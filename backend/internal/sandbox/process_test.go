package sandbox

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type processCommandResolverFake struct {
	command ResolvedProcessCommand
	service string
	profile string
}

func (resolver *processCommandResolverFake) ResolveProcessCommand(
	_ context.Context,
	_ SessionView,
	serviceID, commandID string,
) (ResolvedProcessCommand, error) {
	resolver.service, resolver.profile = serviceID, commandID
	return resolver.command, nil
}

type processStoreFake struct {
	values map[string]ProcessView
	now    time.Time
}

func (store *processStoreFake) Create(_ context.Context, input ProcessRecordInput) (ProcessView, error) {
	if _, exists := store.values[input.ID]; exists {
		return ProcessView{}, ErrProcessExists
	}
	value := ProcessView{
		SchemaVersion: RuntimeProcessSchemaVersion, ID: input.ID,
		ProjectID: input.ProjectID, SessionID: input.SessionID,
		SessionEpoch: input.SessionEpoch, SessionVersionAtCreation: input.SessionVersionAtCreation,
		ActorID: input.ActorID, ServiceID: input.Command.ServiceID, CommandID: input.Command.CommandID,
		TemplateRelease:  input.Command.TemplateRelease,
		WorkingDirectory: input.Command.WorkingDirectory, Argv: append([]string(nil), input.Command.Argv...),
		LogLimitBytes: input.LogLimitBytes, State: ProcessStarting, Version: 1,
		CreatedAt: store.now, UpdatedAt: store.now,
	}
	store.values[value.ID] = value
	return value, nil
}

func (store *processStoreFake) Get(_ context.Context, projectID, sessionID, processID string) (ProcessView, error) {
	value, ok := store.values[processID]
	if !ok || value.ProjectID != projectID || value.SessionID != sessionID {
		return ProcessView{}, ErrProcessNotFound
	}
	return value, nil
}

func (store *processStoreFake) List(_ context.Context, projectID, sessionID string, _ int) ([]ProcessView, error) {
	result := []ProcessView{}
	for _, value := range store.values {
		if value.ProjectID == projectID && value.SessionID == sessionID {
			result = append(result, value)
		}
	}
	return result, nil
}

func (store *processStoreFake) Observe(_ context.Context, input ProcessObservation) (ProcessView, error) {
	value, ok := store.values[input.ProcessID]
	if !ok {
		return ProcessView{}, ErrProcessNotFound
	}
	if value.Version != input.ExpectedProcessVersion {
		return ProcessView{}, ErrProcessVersionConflict
	}
	value.Version++
	value.State = ProcessState(input.Status.State)
	value.PID = input.Status.PID
	value.ExitCode = input.Status.ExitCode
	value.Failure = input.Status.Failure
	value.LogBytes = input.Status.LogBytes
	value.LogTruncated = input.Status.LogTruncated
	startedAt := input.Status.StartedAt
	value.RuntimeStartedAt = &startedAt
	if !input.Status.FinishedAt.IsZero() {
		finishedAt := input.Status.FinishedAt
		value.FinishedAt = &finishedAt
	}
	value.UpdatedAt = store.now.Add(time.Duration(value.Version) * time.Second)
	store.values[value.ID] = value
	return value, nil
}

func (store *processStoreFake) FenceEpoch(
	_ context.Context,
	projectID, sessionID string,
	epoch uint64,
	_, reason string,
) ([]ProcessView, error) {
	result := []ProcessView{}
	for id, value := range store.values {
		if value.ProjectID != projectID || value.SessionID != sessionID || value.SessionEpoch != epoch || !processActive(value.State) {
			continue
		}
		value.State, value.Version, value.Failure = ProcessOrphaned, value.Version+1, reason
		startedAt := value.CreatedAt
		if value.RuntimeStartedAt == nil {
			value.RuntimeStartedAt = &startedAt
		}
		finishedAt := store.now.Add(time.Minute)
		value.FinishedAt = &finishedAt
		store.values[id] = value
		result = append(result, value)
	}
	return result, nil
}

type processRuntimeFake struct {
	startSpec  RuntimeProcessSpec
	signalSpec RuntimeProcessSpec
	signal     string
	status     RuntimeProcessStatus
}

func (runtime *processRuntimeFake) StartProcess(_ context.Context, spec RuntimeProcessSpec) (RuntimeProcessStatus, error) {
	runtime.startSpec = spec
	runtime.status = RuntimeProcessStatus{
		SchemaVersion: RuntimeProcessSchemaVersion, ID: spec.ID, State: ProcessRunning.String(), PID: 210,
		Argv: append([]string(nil), spec.Argv...), WorkingDirectory: spec.WorkingDirectory,
		StartedAt: sandboxBaseTime.Add(10 * time.Second),
	}
	return runtime.status, nil
}

func (runtime *processRuntimeFake) InspectProcess(context.Context, RuntimeProcessSpec) (RuntimeProcessStatus, error) {
	return runtime.status, nil
}

func (runtime *processRuntimeFake) SignalProcess(_ context.Context, spec RuntimeProcessSpec, signal string) (RuntimeProcessStatus, error) {
	runtime.signalSpec, runtime.signal = spec, signal
	exitCode := 143
	runtime.status.State = ProcessFailed.String()
	runtime.status.ExitCode = &exitCode
	runtime.status.Failure = "signal: terminated"
	runtime.status.FinishedAt = runtime.status.StartedAt.Add(time.Second)
	return runtime.status, nil
}

func (runtime *processRuntimeFake) ReadProcessLog(
	_ context.Context,
	spec RuntimeProcessSpec,
	offset, _ int64,
) (RuntimeProcessLog, error) {
	return RuntimeProcessLog{
		SchemaVersion: RuntimeProcessSchemaVersion, ID: spec.ID,
		Offset: offset, NextOffset: offset + 4, Value: []byte("test"),
	}, nil
}

func TestProcessServiceStartsExactResolvedCommandAndControlsIt(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{
		"apps/web/package.json": []byte("{}\n"),
	})
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	view := ready.Snapshot()
	candidates := &controlCandidatesFake{
		record:      repository.CandidateMutationRecord{Candidate: candidate},
		checkpoints: map[string]repository.CandidateSnapshot{},
	}
	sessions := &controlSessionsFake{session: ready, candidates: candidates}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &processCommandResolverFake{command: ResolvedProcessCommand{
		ServiceID: "web", CommandID: "dev", TemplateRelease: view.AllowedServices[1].TemplateRelease,
		WorkingDirectory: "apps/web", Argv: []string{"node", "server.js"},
	}}
	processes := &processStoreFake{values: map[string]ProcessView{}, now: sandboxBaseTime.Add(5 * time.Second)}
	runtime := &processRuntimeFake{}
	events := &lifecycleEventsFake{}
	service, err := newProcessService(
		sessions, candidates, resolver, workspaces, runtime, processes,
		&facadeAccessFake{}, events, 1<<20,
		func() string { return "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" },
		func() time.Time { return sandboxBaseTime.Add(20 * time.Second) },
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(context.Background(), StartProcessInput{
		ProjectID: view.ProjectID, SessionID: view.ID, ActorID: testActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
		ServiceID: "web", CommandID: "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.service != "web" || resolver.profile != "dev" || started.Process.State != ProcessRunning ||
		started.Process.Version != 2 || !reflect.DeepEqual(runtime.startSpec.Argv, []string{"node", "server.js"}) ||
		runtime.startSpec.WorkingDirectory != "apps/web" || runtime.startSpec.Runtime.SessionEpoch != view.SessionEpoch ||
		len(events.inputs) != 1 || events.inputs[0].Channel != ChannelProcess {
		t.Fatalf("exact process start was not preserved: result=%#v spec=%#v events=%#v", started, runtime.startSpec, events.inputs)
	}

	signalled, err := service.Signal(context.Background(), SignalProcessInput{
		ProjectID: view.ProjectID, SessionID: view.ID, ProcessID: started.Process.ID, ActorID: testActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
		ExpectedProcessVersion: started.Process.Version, Signal: "TERM",
	})
	if err != nil || signalled.Process.State != ProcessFailed || signalled.Process.Version != 3 ||
		runtime.signal != "TERM" || runtime.signalSpec.ID != started.Process.ID {
		t.Fatalf("process signal = %#v runtime=%#v err=%v", signalled, runtime, err)
	}
	logs, err := service.Logs(
		context.Background(), view.ProjectID, view.ID, started.Process.ID, testActorID, 0, 1024,
	)
	if err != nil || string(logs.Log.Value) != "test" {
		t.Fatalf("process logs = %#v, %v", logs, err)
	}
}

func TestProcessServicePersistsFailedStart(t *testing.T) {
	service, process, runtime := processFailureFixture(t)
	runtime.startError = ErrRuntimeUnavailable
	result, err := service.Start(context.Background(), process)
	var controlError *ProcessControlError
	if !errors.As(err, &controlError) || !errors.Is(err, ErrRuntimeUnavailable) ||
		result.Process.State != ProcessFailed || result.Process.Version != 2 || result.Process.Failure == "" {
		t.Fatalf("failed process start was not durable: %#v, %v", result, err)
	}
}

type failingProcessRuntime struct {
	processRuntimeFake
	startError error
}

func (runtime *failingProcessRuntime) StartProcess(_ context.Context, spec RuntimeProcessSpec) (RuntimeProcessStatus, error) {
	runtime.startSpec = spec
	return RuntimeProcessStatus{}, runtime.startError
}

func processFailureFixture(t *testing.T) (*ProcessService, StartProcessInput, *failingProcessRuntime) {
	t.Helper()
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"apps/web/package.json": []byte("{}\n")})
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	view := ready.Snapshot()
	candidates := &controlCandidatesFake{
		record:      repository.CandidateMutationRecord{Candidate: candidate},
		checkpoints: map[string]repository.CandidateSnapshot{},
	}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &failingProcessRuntime{}
	service, err := newProcessService(
		&controlSessionsFake{session: ready, candidates: candidates}, candidates,
		&processCommandResolverFake{command: ResolvedProcessCommand{
			ServiceID: "web", CommandID: "dev", TemplateRelease: view.AllowedServices[1].TemplateRelease,
			WorkingDirectory: "apps/web", Argv: []string{"node", "server.js"},
		}},
		workspaces, runtime,
		&processStoreFake{values: map[string]ProcessView{}, now: sandboxBaseTime.Add(5 * time.Second)},
		&facadeAccessFake{}, &lifecycleEventsFake{}, 1<<20,
		func() string { return "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" },
		func() time.Time { return sandboxBaseTime.Add(20 * time.Second) },
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, StartProcessInput{
		ProjectID: view.ProjectID, SessionID: view.ID, ActorID: testActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
		ServiceID: "web", CommandID: "dev",
	}, runtime
}

var _ SandboxProcessStore = (*processStoreFake)(nil)
var _ RuntimeProcessManager = (*processRuntimeFake)(nil)
var _ SessionProcessCommandResolver = (*processCommandResolverFake)(nil)
