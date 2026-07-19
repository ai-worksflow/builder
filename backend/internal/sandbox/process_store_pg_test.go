package sandbox

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresProcessStoreExactLifecycleCanary(t *testing.T) {
	fixture := openSandboxStorePostgresFixture(t)
	sessionID := uuid.New()
	created, err := fixture.store.Create(fixture.context, fixture.sessionInput(sessionID), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	view := created.Snapshot()
	for _, target := range []State{StateStarting, StateReady} {
		advanced, transitionErr := fixture.store.Transition(
			fixture.context, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
			target, fixture.actorID.String(), "process store canary", "",
		)
		if transitionErr != nil {
			t.Fatal(transitionErr)
		}
		view = advanced.Snapshot()
	}
	store, err := NewPostgresProcessStore(fixture.store.database)
	if err != nil {
		t.Fatal(err)
	}
	input := ProcessRecordInput{
		ID: uuid.NewString(), ProjectID: view.ProjectID, SessionID: view.ID,
		SessionEpoch: view.SessionEpoch, SessionVersionAtCreation: view.Version,
		ActorID: fixture.actorID.String(), LogLimitBytes: 1 << 20,
		Command: ResolvedProcessCommand{
			ServiceID: "web-ui", CommandID: "dev", TemplateRelease: fixture.webRelease,
			WorkingDirectory: "apps/web", Argv: []string{"node", "server.js"},
		},
	}
	process, err := store.Create(fixture.context, input)
	if err != nil {
		t.Fatalf("create exact process: %v", err)
	}
	if process.State != ProcessStarting || process.Version != 1 || process.SessionEpoch != view.SessionEpoch ||
		process.WorkingDirectory != "apps/web" || len(process.Argv) != 2 {
		t.Fatalf("unexpected starting process: %#v", process)
	}

	wrong := input
	wrong.ID = uuid.NewString()
	wrong.Command.Argv = []string{"node", "caller-selected.js"}
	if _, err := store.Create(fixture.context, wrong); err == nil {
		t.Fatal("caller-selected argv bypassed the exact release command")
	}

	startedAt := time.Now().UTC().Add(time.Millisecond)
	runningStatus := RuntimeProcessStatus{
		SchemaVersion: RuntimeProcessSchemaVersion, ID: process.ID, State: ProcessRunning.String(), PID: 321,
		Argv: append([]string(nil), process.Argv...), WorkingDirectory: process.WorkingDirectory,
		StartedAt: startedAt,
	}
	process, err = store.Observe(fixture.context, ProcessObservation{
		ProjectID: view.ProjectID, SessionID: view.ID, ProcessID: process.ID,
		ActorID: fixture.actorID.String(), ExpectedProcessVersion: process.Version,
		ExpectedSessionEpoch: view.SessionEpoch, EventKind: "runtime.observed",
		Reason: "supervisor running", Status: runningStatus,
	})
	if err != nil || process.State != ProcessRunning || process.Version != 2 || process.PID != 321 {
		t.Fatalf("observe running = %#v, %v", process, err)
	}
	if _, err := store.Observe(fixture.context, ProcessObservation{
		ProjectID: view.ProjectID, SessionID: view.ID, ProcessID: process.ID,
		ActorID: fixture.actorID.String(), ExpectedProcessVersion: 1,
		ExpectedSessionEpoch: view.SessionEpoch, EventKind: "runtime.observed",
		Reason: "stale observation", Status: runningStatus,
	}); !errors.Is(err, ErrProcessVersionConflict) {
		t.Fatalf("stale process version error = %v", err)
	}

	process, err = store.Observe(fixture.context, ProcessObservation{
		ProjectID: view.ProjectID, SessionID: view.ID, ProcessID: process.ID,
		ActorID: fixture.actorID.String(), ExpectedProcessVersion: process.Version,
		ExpectedSessionEpoch: view.SessionEpoch, EventKind: "signal.sent", Signal: "TERM",
		Reason: "TERM delivered", Status: runningStatus,
	})
	if err != nil || process.Version != 3 || process.State != ProcessRunning {
		t.Fatalf("record signal = %#v, %v", process, err)
	}
	exitCode := 0
	exitedStatus := runningStatus
	exitedStatus.State = ProcessExited.String()
	exitedStatus.ExitCode = &exitCode
	exitedStatus.FinishedAt = startedAt.Add(time.Second)
	exitedStatus.LogBytes = 24
	process, err = store.Observe(fixture.context, ProcessObservation{
		ProjectID: view.ProjectID, SessionID: view.ID, ProcessID: process.ID,
		ActorID: fixture.actorID.String(), ExpectedProcessVersion: process.Version,
		ExpectedSessionEpoch: view.SessionEpoch, EventKind: "runtime.observed",
		Reason: "supervisor exited", Status: exitedStatus,
	})
	if err != nil || process.State != ProcessExited || process.Version != 4 || process.ExitCode == nil || *process.ExitCode != 0 {
		t.Fatalf("observe exit = %#v, %v", process, err)
	}

	orphanInput := input
	orphanInput.ID = uuid.NewString()
	orphaned, err := store.Create(fixture.context, orphanInput)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := store.FenceEpoch(
		fixture.context, view.ProjectID, view.ID, view.SessionEpoch,
		fixture.actorID.String(), "old runtime was terminated",
	)
	if err != nil || len(fenced) != 1 || fenced[0].ID != orphaned.ID || fenced[0].State != ProcessOrphaned {
		t.Fatalf("fence active epoch = %#v, %v", fenced, err)
	}
	listed, err := store.List(fixture.context, view.ProjectID, view.ID, 10)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list processes = %#v, %v", listed, err)
	}
}
