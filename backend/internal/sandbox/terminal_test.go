package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type terminalStoreFake struct {
	mu     sync.Mutex
	values map[string]TerminalView
	now    time.Time
}

func (store *terminalStoreFake) Create(_ context.Context, input TerminalRecordInput) (TerminalView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value := TerminalView{
		SchemaVersion: TerminalSchemaVersion, ID: input.ID, ProjectID: input.ProjectID,
		SessionID: input.SessionID, SessionEpoch: input.SessionEpoch,
		SessionVersionAtCreation: input.SessionVersionAtCreation, ActorID: input.ActorID,
		WorkingDirectory: input.WorkingDirectory, ShellPath: "/bin/bash",
		Rows: input.Rows, Columns: input.Columns, OutputLimitBytes: input.OutputLimitBytes,
		State: TerminalOpening, Version: 1, CreatedAt: store.now, UpdatedAt: store.now,
	}
	store.values[value.ID] = value
	return value, nil
}

func (store *terminalStoreFake) Get(_ context.Context, projectID, sessionID, terminalID string) (TerminalView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.values[terminalID]
	if !exists || value.ProjectID != projectID || value.SessionID != sessionID {
		return TerminalView{}, ErrTerminalNotFound
	}
	return value, nil
}

func (store *terminalStoreFake) List(_ context.Context, projectID, sessionID string, _ int) ([]TerminalView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := []TerminalView{}
	for _, value := range store.values {
		if value.ProjectID == projectID && value.SessionID == sessionID {
			result = append(result, value)
		}
	}
	return result, nil
}

func (store *terminalStoreFake) Transition(_ context.Context, input TerminalTransitionInput) (TerminalView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.values[input.TerminalID]
	if !exists {
		return TerminalView{}, ErrTerminalNotFound
	}
	if value.Version != input.ExpectedVersion {
		return TerminalView{}, ErrTerminalVersionConflict
	}
	value.Version++
	value.State, value.Rows, value.Columns = input.State, input.Rows, input.Columns
	value.ExitCode, value.Failure = input.ExitCode, input.Failure
	value.OutputBytes, value.OutputTruncated = input.OutputBytes, input.OutputTruncated
	value.RuntimeStartedAt, value.FinishedAt = input.RuntimeStartedAt, input.FinishedAt
	value.UpdatedAt = store.now.Add(time.Duration(value.Version) * time.Second)
	store.values[value.ID] = value
	return value, nil
}

func (store *terminalStoreFake) FenceEpoch(
	_ context.Context,
	projectID, sessionID string,
	epoch uint64,
	_, reason string,
) ([]TerminalView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := []TerminalView{}
	for id, value := range store.values {
		if value.ProjectID != projectID || value.SessionID != sessionID || value.SessionEpoch != epoch ||
			(value.State != TerminalOpening && value.State != TerminalRunning) {
			continue
		}
		value.Version++
		value.State, value.Failure = TerminalOrphaned, reason
		started, finished := value.CreatedAt, store.now.Add(time.Minute)
		if value.RuntimeStartedAt == nil {
			value.RuntimeStartedAt = &started
		}
		value.FinishedAt = &finished
		store.values[id] = value
		result = append(result, value)
	}
	return result, nil
}

type runtimeTerminalFake struct {
	mu      sync.Mutex
	inputs  [][]byte
	rows    uint16
	columns uint16
	signal  string
	closed  bool
	output  chan []byte
	done    chan RuntimeTerminalExit
}

func (terminal *runtimeTerminalFake) WriteInput(value []byte) error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.inputs = append(terminal.inputs, append([]byte(nil), value...))
	return nil
}
func (terminal *runtimeTerminalFake) Resize(rows, columns uint16) error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.rows, terminal.columns = rows, columns
	return nil
}
func (terminal *runtimeTerminalFake) Signal(value string) error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.signal = value
	return nil
}
func (terminal *runtimeTerminalFake) Close() error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.closed = true
	return nil
}
func (terminal *runtimeTerminalFake) Output() <-chan []byte            { return terminal.output }
func (terminal *runtimeTerminalFake) Done() <-chan RuntimeTerminalExit { return terminal.done }

type terminalRuntimeFake struct {
	spec     RuntimeTerminalSpec
	terminal *runtimeTerminalFake
}

func (runtime *terminalRuntimeFake) OpenTerminal(_ context.Context, spec RuntimeTerminalSpec) (RuntimeTerminal, error) {
	runtime.spec = spec
	return runtime.terminal, nil
}

type terminalEventsFake struct {
	mu     sync.Mutex
	inputs []StreamEventInput
	notify chan StreamEventInput
}

func (events *terminalEventsFake) Publish(_ context.Context, input StreamEventInput) (StreamEnvelope, error) {
	events.mu.Lock()
	events.inputs = append(events.inputs, input)
	sequence := len(events.inputs)
	events.mu.Unlock()
	select {
	case events.notify <- input:
	default:
	}
	return StreamEnvelope{Sequence: uint64(sequence)}, nil
}
func (*terminalEventsFake) Replay(context.Context, string, uint64, StreamChannel, uint64, int) ([]StreamEnvelope, uint64, error) {
	return nil, 0, nil
}
func (*terminalEventsFake) ReadAfter(context.Context, string, uint64, StreamChannel, uint64, int, time.Duration) ([]StreamEnvelope, error) {
	return nil, nil
}

func TestTerminalServiceOpensFixedShellControlsAndRedactsOutput(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"README.md": []byte("terminal\n")})
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
	runtimeTerminal := &runtimeTerminalFake{output: make(chan []byte, 4), done: make(chan RuntimeTerminalExit, 1)}
	runtime := &terminalRuntimeFake{terminal: runtimeTerminal}
	store := &terminalStoreFake{values: map[string]TerminalView{}, now: sandboxBaseTime.Add(5 * time.Second)}
	events := &terminalEventsFake{notify: make(chan StreamEventInput, 16)}
	service, err := newTerminalService(
		&controlSessionsFake{session: ready, candidates: candidates}, candidates, workspaces,
		runtime, store, &facadeAccessFake{}, events, 1<<20,
		func() string { return "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" },
		func() time.Time { return sandboxBaseTime.Add(20 * time.Second) },
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	result, err := service.Create(context.Background(), CreateTerminalInput{
		ProjectID: view.ProjectID, SessionID: view.ID, ActorID: testActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
		WorkingDirectory: ".", Rows: 24, Columns: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Terminal.State != TerminalRunning || result.Terminal.ShellPath != "/bin/bash" ||
		runtime.spec.WorkingDirectory != "." || runtime.spec.Runtime.SessionEpoch != view.SessionEpoch {
		t.Fatalf("terminal did not preserve fixed runtime shape: result=%#v spec=%#v", result, runtime.spec)
	}
	control := TerminalStreamControl{
		ProjectID: view.ProjectID, SessionID: view.ID, SessionEpoch: view.SessionEpoch,
		ActorID: testActorID, TerminalID: result.Terminal.ID,
	}
	control.RequestID = "11111111-1111-4111-8111-111111111111"
	if err := service.AttachTerminal(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	control.RequestID = "22222222-2222-4222-8222-222222222222"
	if err := service.WriteTerminal(context.Background(), control, []byte("pwd\r")); err != nil {
		t.Fatal(err)
	}
	control.RequestID = "33333333-3333-4333-8333-333333333333"
	if err := service.ResizeTerminal(context.Background(), control, 40, 120); err != nil {
		t.Fatal(err)
	}
	control.RequestID = "44444444-4444-4444-8444-444444444444"
	if err := service.SignalTerminal(context.Background(), control, "INT"); err != nil {
		t.Fatal(err)
	}
	control.RequestID = "55555555-5555-4555-8555-555555555555"
	if err := service.DetachTerminal(context.Background(), control); err != nil {
		t.Fatal(err)
	}

	runtimeTerminal.output <- []byte("token sk-1234567890abcdefghijklmnop\r\n")
	var output StreamEventInput
	deadline := time.After(2 * time.Second)
	for output.EventType != "pty.output" {
		select {
		case output = <-events.notify:
		case <-deadline:
			t.Fatal("PTY output event was not published")
		}
	}
	var payload struct {
		Value string `json:"valueBase64"`
	}
	if err := json.Unmarshal(output.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(payload.Value)
	if err != nil || strings.Contains(string(decoded), "sk-123") || !strings.Contains(string(decoded), "[REDACTED TOKEN]") {
		t.Fatalf("terminal output was not redacted: %q err=%v", decoded, err)
	}

	close(runtimeTerminal.output)
	runtimeTerminal.done <- RuntimeTerminalExit{ExitCode: 0, FinishedAt: sandboxBaseTime.Add(time.Minute)}
	close(runtimeTerminal.done)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		terminal, _ := store.Get(context.Background(), view.ProjectID, view.ID, result.Terminal.ID)
		if terminal.State == TerminalExited {
			if terminal.OutputBytes == 0 || terminal.Version < 6 {
				t.Fatalf("terminal exit accounting is incomplete: %#v", terminal)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal exit was not persisted")
}

func TestTerminalOutputRedactionDoesNotExposeCredentialValue(t *testing.T) {
	value := redactTerminalOutput([]byte("api_key=abcdefghijklmnopqrstuv"))
	if strings.Contains(string(value), "abcdefghijkl") || string(value) != "api_key=[REDACTED]" {
		t.Fatalf("credential redaction failed: %q", value)
	}
}

func TestCurrentSessionTerminalsExcludeHistoricalEpochs(t *testing.T) {
	values := []TerminalView{
		{ID: "current-1", SessionEpoch: 2},
		{ID: "historical", SessionEpoch: 1},
		{ID: "current-2", SessionEpoch: 2},
	}
	current := currentSessionTerminals(values, 2, 1)
	if len(current) != 1 || current[0].ID != "current-1" {
		t.Fatalf("current terminal projection = %#v", current)
	}
	current = currentSessionTerminals(values, 2, 0)
	if len(current) != 2 || current[0].ID != "current-1" || current[1].ID != "current-2" {
		t.Fatalf("default current terminal projection = %#v", current)
	}
}

var _ SandboxTerminalStore = (*terminalStoreFake)(nil)
var _ RuntimeTerminalManager = (*terminalRuntimeFake)(nil)
var _ RuntimeTerminal = (*runtimeTerminalFake)(nil)
var _ StreamEventStore = (*terminalEventsFake)(nil)
