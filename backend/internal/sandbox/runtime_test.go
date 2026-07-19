package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

type lifecycleSessionStoreFake struct {
	session     SandboxSession
	transitions []State
}

func (store *lifecycleSessionStoreFake) Get(context.Context, string, string) (SandboxSession, error) {
	return store.session.Clone(), nil
}

func (store *lifecycleSessionStoreFake) Transition(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	target State,
	_, reason, _ string,
) (SandboxSession, error) {
	view := store.session.Snapshot()
	now := view.UpdatedAt.Add(time.Second)
	var next SandboxSession
	var err error
	switch target {
	case StateStarting:
		next, err = store.session.BeginStart(expectedVersion, expectedEpoch, now)
	case StateReady:
		next, err = store.session.MarkReady(expectedVersion, expectedEpoch, now)
	case StateFailed:
		next, err = store.session.Fail(expectedVersion, expectedEpoch, reason, now)
	default:
		err = ErrInvalidTransition
	}
	if err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	store.transitions = append(store.transitions, target)
	return next.Clone(), nil
}

type lifecycleRuntimeFake struct {
	ensureCalls    int
	startCalls     int
	waitCalls      int
	terminateCalls int
	waitError      error
}

func (runtime *lifecycleRuntimeFake) Ensure(_ context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runtime.ensureCalls++
	return lifecycleRuntimeStatus(spec, "created", false), nil
}

func (runtime *lifecycleRuntimeFake) Start(_ context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runtime.startCalls++
	return lifecycleRuntimeStatus(spec, "running", false), nil
}

func (runtime *lifecycleRuntimeFake) WaitReady(_ context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runtime.waitCalls++
	if runtime.waitError != nil {
		return RuntimeStatus{}, runtime.waitError
	}
	return lifecycleRuntimeStatus(spec, "running", true), nil
}

func (*lifecycleRuntimeFake) Suspend(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected suspend")
}

func (*lifecycleRuntimeFake) Resume(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected resume")
}

func (runtime *lifecycleRuntimeFake) Terminate(context.Context, RuntimeSpec) error {
	runtime.terminateCalls++
	return nil
}

func (*lifecycleRuntimeFake) Inspect(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected inspect")
}

type lifecycleEventsFake struct {
	inputs []StreamEventInput
}

func (events *lifecycleEventsFake) Publish(_ context.Context, input StreamEventInput) (StreamEnvelope, error) {
	events.inputs = append(events.inputs, input)
	return StreamEnvelope{Sequence: uint64(len(events.inputs))}, nil
}

func (*lifecycleEventsFake) Replay(context.Context, string, uint64, StreamChannel, uint64, int) ([]StreamEnvelope, uint64, error) {
	return nil, 0, nil
}

func (*lifecycleEventsFake) ReadAfter(context.Context, string, uint64, StreamChannel, uint64, int, time.Duration) ([]StreamEnvelope, error) {
	return nil, nil
}

func TestLifecycleServiceProjectsStartsAndPublishesReadySession(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"README.md": []byte("runtime workspace\n")})
	initial := newTestSession(t, candidate, sandboxBaseTime)
	sessions := &lifecycleSessionStoreFake{session: initial}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &lifecycleRuntimeFake{}
	events := &lifecycleEventsFake{}
	service, err := NewLifecycleService(sessions, workspaces, runtime, events)
	if err != nil {
		t.Fatal(err)
	}

	view, err := service.Start(context.Background(), initial.Snapshot(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StateReady || view.Version != 3 || runtime.ensureCalls != 1 || runtime.startCalls != 1 ||
		runtime.waitCalls != 1 || runtime.terminateCalls != 0 {
		t.Fatalf("lifecycle did not become ready: view=%#v runtime=%#v", view, runtime)
	}
	if len(sessions.transitions) != 2 || sessions.transitions[0] != StateStarting || sessions.transitions[1] != StateReady {
		t.Fatalf("lifecycle transitions = %v", sessions.transitions)
	}
	if len(events.inputs) != 2 || events.inputs[0].EventType != "session.state" ||
		events.inputs[0].AggregateVersion != 2 || events.inputs[1].AggregateVersion != 3 {
		t.Fatalf("lifecycle stream events = %#v", events.inputs)
	}
}

func TestLifecycleServiceCleansRuntimeAndPersistsFailure(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"README.md": []byte("runtime workspace\n")})
	initial := newTestSession(t, candidate, sandboxBaseTime)
	sessions := &lifecycleSessionStoreFake{session: initial}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &lifecycleRuntimeFake{waitError: ErrRuntimeNotReady}
	events := &lifecycleEventsFake{}
	service, err := NewLifecycleService(sessions, workspaces, runtime, events)
	if err != nil {
		t.Fatal(err)
	}

	view, err := service.Start(context.Background(), initial.Snapshot(), candidate)
	var bootstrapError *RuntimeBootstrapError
	if !errors.As(err, &bootstrapError) || !errors.Is(err, ErrRuntimeNotReady) {
		t.Fatalf("runtime failure was not preserved: %v", err)
	}
	if view.State != StateFailed || view.FailureReason != "runtime bootstrap failed" || runtime.terminateCalls != 1 {
		t.Fatalf("failed runtime was not cleaned and recorded: view=%#v runtime=%#v", view, runtime)
	}
	if len(sessions.transitions) != 2 || sessions.transitions[0] != StateStarting || sessions.transitions[1] != StateFailed ||
		len(events.inputs) != 2 || events.inputs[1].AggregateVersion != view.Version {
		t.Fatalf("failure transitions/events = %v / %#v", sessions.transitions, events.inputs)
	}
}

func lifecycleRuntimeStatus(spec RuntimeSpec, state string, healthy bool) RuntimeStatus {
	return RuntimeStatus{
		ID: "runtime", SessionID: spec.SessionID, SessionEpoch: spec.SessionEpoch,
		State: state, Healthy: healthy,
	}
}

var _ LifecycleSessionStore = (*lifecycleSessionStoreFake)(nil)
var _ RuntimeManager = (*lifecycleRuntimeFake)(nil)
var _ StreamEventStore = (*lifecycleEventsFake)(nil)
