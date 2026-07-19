package sandbox

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresTerminalStoreFencedLifecycleCanary(t *testing.T) {
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
			target, fixture.actorID.String(), "terminal store canary", "",
		)
		if transitionErr != nil {
			t.Fatal(transitionErr)
		}
		view = advanced.Snapshot()
	}
	store, err := NewPostgresTerminalStore(fixture.store.database)
	if err != nil {
		t.Fatal(err)
	}
	input := TerminalRecordInput{
		ID: uuid.NewString(), ProjectID: view.ProjectID, SessionID: view.ID,
		SessionEpoch: view.SessionEpoch, SessionVersionAtCreation: view.Version,
		ActorID: fixture.actorID.String(), WorkingDirectory: ".",
		Rows: 24, Columns: 80, OutputLimitBytes: 1 << 20,
	}
	terminal, err := store.Create(fixture.context, input)
	if err != nil {
		t.Fatalf("create exact terminal: %v", err)
	}
	if terminal.State != TerminalOpening || terminal.Version != 1 || terminal.ShellPath != "/bin/bash" {
		t.Fatalf("unexpected opening terminal: %#v", terminal)
	}

	startedAt := time.Now().UTC().Add(time.Millisecond)
	terminal, err = store.Transition(fixture.context, terminalTransition(
		terminal, fixture.actorID.String(), uuid.NewString(), "runtime.opened", "fixed shell opened",
		TerminalRunning, 24, 80, nil, "", 0, false, &startedAt, nil,
	))
	if err != nil || terminal.State != TerminalRunning || terminal.Version != 2 {
		t.Fatalf("open terminal = %#v, %v", terminal, err)
	}

	stale := terminalTransition(
		terminal, fixture.actorID.String(), uuid.NewString(), "resized", "stale resize",
		TerminalRunning, 40, 120, nil, "", 0, false, &startedAt, nil,
	)
	stale.ExpectedVersion = 1
	if _, err := store.Transition(fixture.context, stale); !errors.Is(err, ErrTerminalVersionConflict) {
		t.Fatalf("stale terminal version error = %v", err)
	}

	terminal, err = store.Transition(fixture.context, terminalTransition(
		terminal, fixture.actorID.String(), uuid.NewString(), "resized", "browser resized terminal",
		TerminalRunning, 40, 120, nil, "", 0, false, &startedAt, nil,
	))
	if err != nil || terminal.Version != 3 || terminal.Rows != 40 || terminal.Columns != 120 {
		t.Fatalf("resize terminal = %#v, %v", terminal, err)
	}

	signal := terminalTransition(
		terminal, fixture.actorID.String(), uuid.NewString(), "signal.sent", "interrupt delivered",
		TerminalRunning, 40, 120, nil, "", 0, false, &startedAt, nil,
	)
	signal.Signal = "INT"
	terminal, err = store.Transition(fixture.context, signal)
	if err != nil || terminal.Version != 4 || terminal.State != TerminalRunning {
		t.Fatalf("signal terminal = %#v, %v", terminal, err)
	}

	exitCode := 0
	finishedAt := startedAt.Add(time.Second)
	terminal, err = store.Transition(fixture.context, terminalTransition(
		terminal, fixture.actorID.String(), terminal.ID, "runtime.exited", "fixed shell exited",
		TerminalExited, 40, 120, &exitCode, "", 16, false, &startedAt, &finishedAt,
	))
	if err != nil || terminal.State != TerminalExited || terminal.Version != 5 ||
		terminal.ExitCode == nil || *terminal.ExitCode != 0 || terminal.OutputBytes != 16 {
		t.Fatalf("exit terminal = %#v, %v", terminal, err)
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
	if err != nil || len(fenced) != 1 || fenced[0].ID != orphaned.ID || fenced[0].State != TerminalOrphaned {
		t.Fatalf("fence active terminals = %#v, %v", fenced, err)
	}
	listed, err := store.List(fixture.context, view.ProjectID, view.ID, 10)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list terminals = %#v, %v", listed, err)
	}
}
