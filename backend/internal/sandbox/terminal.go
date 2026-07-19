package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const TerminalSchemaVersion = "sandbox-terminal/v1"

var (
	ErrTerminalInvalid           = errors.New("invalid sandbox terminal request")
	ErrTerminalUnavailable       = errors.New("sandbox terminal control is not configured")
	ErrTerminalNotFound          = errors.New("sandbox terminal was not found")
	ErrTerminalExists            = errors.New("sandbox terminal already exists")
	ErrTerminalVersionConflict   = errors.New("sandbox terminal version conflict")
	ErrTerminalInvalidTransition = errors.New("invalid sandbox terminal state transition")
	ErrTerminalStoreIntegrity    = errors.New("sandbox terminal persistence integrity failure")
	ErrTerminalStoreUnavailable  = errors.New("sandbox terminal persistence is unavailable")
)

var (
	terminalPrivateKeyPattern = regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)
	terminalTokenPattern      = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{16,}|(?:gh[pousr]|github_pat)_[a-z0-9_-]{16,}|AKIA[0-9A-Z]{16})\b`)
	terminalCredentialPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|client[_-]?secret|auth[_-]?token|password|authorization)\b\s*[:=]\s*([^\s]{12,})`)
)

type TerminalState string

const (
	TerminalOpening  TerminalState = "opening"
	TerminalRunning  TerminalState = "running"
	TerminalExited   TerminalState = "exited"
	TerminalFailed   TerminalState = "failed"
	TerminalOrphaned TerminalState = "orphaned"
)

type TerminalView struct {
	SchemaVersion            string        `json:"schemaVersion"`
	ID                       string        `json:"id"`
	ProjectID                string        `json:"projectId"`
	SessionID                string        `json:"sessionId"`
	SessionEpoch             uint64        `json:"sessionEpoch"`
	SessionVersionAtCreation uint64        `json:"sessionVersionAtCreation"`
	ActorID                  string        `json:"actorId"`
	WorkingDirectory         string        `json:"workingDirectory"`
	ShellPath                string        `json:"shellPath"`
	Rows                     uint16        `json:"rows"`
	Columns                  uint16        `json:"columns"`
	OutputLimitBytes         int64         `json:"outputLimitBytes"`
	State                    TerminalState `json:"state"`
	Version                  uint64        `json:"version"`
	ExitCode                 *int          `json:"exitCode,omitempty"`
	Failure                  string        `json:"failure,omitempty"`
	OutputBytes              int64         `json:"outputBytes"`
	OutputTruncated          bool          `json:"outputTruncated"`
	RuntimeStartedAt         *time.Time    `json:"runtimeStartedAt,omitempty"`
	FinishedAt               *time.Time    `json:"finishedAt,omitempty"`
	CreatedAt                time.Time     `json:"createdAt"`
	UpdatedAt                time.Time     `json:"updatedAt"`
}

type TerminalRecordInput struct {
	ID                       string
	ProjectID                string
	SessionID                string
	SessionEpoch             uint64
	SessionVersionAtCreation uint64
	ActorID                  string
	WorkingDirectory         string
	Rows                     uint16
	Columns                  uint16
	OutputLimitBytes         int64
}

type TerminalTransitionInput struct {
	ProjectID        string
	SessionID        string
	TerminalID       string
	SessionEpoch     uint64
	ExpectedVersion  uint64
	ActorID          string
	RequestID        string
	EventKind        string
	Signal           string
	Reason           string
	State            TerminalState
	Rows             uint16
	Columns          uint16
	ExitCode         *int
	Failure          string
	OutputBytes      int64
	OutputTruncated  bool
	RuntimeStartedAt *time.Time
	FinishedAt       *time.Time
}

type SandboxTerminalStore interface {
	Create(context.Context, TerminalRecordInput) (TerminalView, error)
	Get(context.Context, string, string, string) (TerminalView, error)
	List(context.Context, string, string, int) ([]TerminalView, error)
	Transition(context.Context, TerminalTransitionInput) (TerminalView, error)
	FenceEpoch(context.Context, string, string, uint64, string, string) ([]TerminalView, error)
}

type CreateTerminalInput struct {
	ProjectID              string
	SessionID              string
	ActorID                string
	ExpectedSessionVersion uint64
	ExpectedSessionEpoch   uint64
	WorkingDirectory       string
	Rows                   uint16
	Columns                uint16
}

type TerminalResult struct {
	Session  SessionView  `json:"session"`
	Terminal TerminalView `json:"terminal"`
}

type TerminalList struct {
	Session   SessionView    `json:"session"`
	Terminals []TerminalView `json:"terminals"`
}

type TerminalStreamControl struct {
	ProjectID    string
	SessionID    string
	SessionEpoch uint64
	ActorID      string
	TerminalID   string
	RequestID    string
}

type TerminalStreamController interface {
	AttachTerminal(context.Context, TerminalStreamControl) error
	WriteTerminal(context.Context, TerminalStreamControl, []byte) error
	ResizeTerminal(context.Context, TerminalStreamControl, uint16, uint16) error
	SignalTerminal(context.Context, TerminalStreamControl, string) error
	DetachTerminal(context.Context, TerminalStreamControl) error
}

type managedTerminal struct {
	mu        sync.Mutex
	view      TerminalView
	runtime   RuntimeTerminal
	cancel    context.CancelFunc
	output    int64
	truncated bool
}

type TerminalService struct {
	sessions    ControlSessionStore
	candidates  CandidateControls
	workspaces  *WorkspaceMaterializer
	runtime     RuntimeTerminalManager
	terminals   SandboxTerminalStore
	access      ProjectAuthorizer
	events      StreamEventStore
	outputLimit int64
	newID       func() string
	now         func() time.Time
	mu          sync.RWMutex
	active      map[string]*managedTerminal
	closed      bool
}

func NewTerminalService(
	sessions ControlSessionStore,
	candidates CandidateControls,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeTerminalManager,
	terminals SandboxTerminalStore,
	access ProjectAuthorizer,
	events StreamEventStore,
	outputLimit int64,
) (*TerminalService, error) {
	return newTerminalService(
		sessions, candidates, workspaces, runtime, terminals, access, events,
		outputLimit, uuid.NewString, time.Now,
	)
}

func newTerminalService(
	sessions ControlSessionStore,
	candidates CandidateControls,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeTerminalManager,
	terminals SandboxTerminalStore,
	access ProjectAuthorizer,
	events StreamEventStore,
	outputLimit int64,
	newID func() string,
	now func() time.Time,
) (*TerminalService, error) {
	if sessions == nil || candidates == nil || workspaces == nil || runtime == nil || terminals == nil ||
		access == nil || events == nil || newID == nil || now == nil || outputLimit < 1024 || outputLimit > 64<<20 {
		return nil, errors.New("sandbox terminal stores, runtime, access, events, and bounded output policy are required")
	}
	return &TerminalService{
		sessions: sessions, candidates: candidates, workspaces: workspaces, runtime: runtime,
		terminals: terminals, access: access, events: events, outputLimit: outputLimit,
		newID: newID, now: now, active: map[string]*managedTerminal{},
	}, nil
}

func (service *TerminalService) Create(ctx context.Context, input CreateTerminalInput) (TerminalResult, error) {
	if err := service.validateCreate(ctx, input); err != nil {
		return TerminalResult{}, err
	}
	if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
		return TerminalResult{}, fmt.Errorf("authorize sandbox terminal open: %w", err)
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return TerminalResult{}, err
	}
	if err := session.Authorize(ActionPTY, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
		return TerminalResult{}, err
	}
	view := session.Snapshot()
	record, err := service.candidates.Get(ctx, view.ProjectID, view.Candidate.ID)
	if err != nil {
		return TerminalResult{}, err
	}
	if !candidateProjectionMatches(view.Candidate, record.Candidate) {
		return TerminalResult{}, ErrSessionProjectionStale
	}
	mount, err := service.workspaces.Materialize(ctx, view, record.Candidate)
	if err != nil {
		return TerminalResult{}, err
	}
	runtimeSpec, err := RuntimeSpecForSession(view, mount)
	if err != nil {
		return TerminalResult{}, err
	}
	id := service.newID()
	if !validUUID(id) {
		return TerminalResult{}, ErrTerminalInvalid
	}
	terminal, err := service.terminals.Create(ctx, TerminalRecordInput{
		ID: id, ProjectID: view.ProjectID, SessionID: view.ID, SessionEpoch: view.SessionEpoch,
		SessionVersionAtCreation: view.Version, ActorID: input.ActorID,
		WorkingDirectory: input.WorkingDirectory, Rows: input.Rows, Columns: input.Columns,
		OutputLimitBytes: service.outputLimit,
	})
	if err != nil {
		return TerminalResult{}, err
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	runtimeTerminal, err := service.runtime.OpenTerminal(runtimeCtx, RuntimeTerminalSpec{
		Runtime: runtimeSpec, ID: terminal.ID, WorkingDirectory: terminal.WorkingDirectory,
		Rows: terminal.Rows, Columns: terminal.Columns,
	})
	if err != nil {
		cancel()
		failed := service.failOpen(ctx, terminal, input.ActorID, err)
		service.publishLifecycle(ctx, failed, "pty.failed", terminal.ID)
		return TerminalResult{Session: view, Terminal: failed}, err
	}
	managed := &managedTerminal{view: terminal, runtime: runtimeTerminal, cancel: cancel}
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		_ = runtimeTerminal.Close()
		cancel()
		return TerminalResult{}, ErrTerminalUnavailable
	}
	service.active[terminal.ID] = managed
	service.mu.Unlock()

	now := service.now().UTC()
	opened, err := service.terminals.Transition(ctx, terminalTransition(
		terminal, input.ActorID, terminal.ID, "runtime.opened", "fixed non-root terminal opened",
		TerminalRunning, terminal.Rows, terminal.Columns, nil, "", 0, false, &now, nil,
	))
	if err != nil {
		service.removeActive(terminal.ID, managed)
		_ = runtimeTerminal.Close()
		cancel()
		return TerminalResult{Session: view, Terminal: terminal}, err
	}
	managed.view = opened
	service.publishLifecycle(ctx, opened, "pty.opened", terminal.ID)
	go service.pump(managed)
	return TerminalResult{Session: view, Terminal: opened}, nil
}

func (service *TerminalService) Get(
	ctx context.Context,
	projectID, sessionID, terminalID, actorID string,
) (TerminalResult, error) {
	if service == nil || ctx == nil || !validUUID(projectID) || !validUUID(sessionID) ||
		!validUUID(terminalID) || !validUUID(actorID) {
		return TerminalResult{}, ErrTerminalInvalid
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return TerminalResult{}, fmt.Errorf("authorize sandbox terminal view: %w", err)
	}
	session, err := service.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return TerminalResult{}, err
	}
	terminal, err := service.terminals.Get(ctx, projectID, sessionID, terminalID)
	if err != nil {
		return TerminalResult{}, err
	}
	return TerminalResult{Session: session.Snapshot(), Terminal: terminal}, nil
}

func (service *TerminalService) List(
	ctx context.Context,
	projectID, sessionID, actorID string,
	limit int,
) (TerminalList, error) {
	if service == nil || ctx == nil || !validUUID(projectID) || !validUUID(sessionID) || !validUUID(actorID) {
		return TerminalList{}, ErrTerminalInvalid
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return TerminalList{}, fmt.Errorf("authorize sandbox terminal list: %w", err)
	}
	session, err := service.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return TerminalList{}, err
	}
	terminals, err := service.terminals.List(ctx, projectID, sessionID, limit)
	if err != nil {
		return TerminalList{}, err
	}
	return TerminalList{Session: session.Snapshot(), Terminals: terminals}, nil
}

func (service *TerminalService) AttachTerminal(ctx context.Context, input TerminalStreamControl) error {
	managed, err := service.authorizedManaged(ctx, input)
	if err != nil {
		return err
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	return service.recordControl(ctx, managed, input, "attached", "terminal stream attached", "", managed.view.Rows, managed.view.Columns)
}

func (service *TerminalService) WriteTerminal(
	ctx context.Context,
	input TerminalStreamControl,
	value []byte,
) error {
	managed, err := service.authorizedManaged(ctx, input)
	if err != nil {
		return err
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if managed.view.State != TerminalRunning {
		return ErrTerminalInvalidTransition
	}
	return managed.runtime.WriteInput(value)
}

func (service *TerminalService) ResizeTerminal(
	ctx context.Context,
	input TerminalStreamControl,
	rows, columns uint16,
) error {
	if !validTerminalSize(rows, columns) {
		return ErrTerminalInvalid
	}
	managed, err := service.authorizedManaged(ctx, input)
	if err != nil {
		return err
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if err := managed.runtime.Resize(rows, columns); err != nil {
		return err
	}
	return service.recordControl(ctx, managed, input, "resized", "terminal dimensions changed", "", rows, columns)
}

func (service *TerminalService) SignalTerminal(
	ctx context.Context,
	input TerminalStreamControl,
	signal string,
) error {
	signal = strings.ToUpper(strings.TrimSpace(signal))
	if !validTerminalSignal(signal) {
		return ErrTerminalInvalid
	}
	managed, err := service.authorizedManaged(ctx, input)
	if err != nil {
		return err
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if err := managed.runtime.Signal(signal); err != nil {
		return err
	}
	return service.recordControl(ctx, managed, input, "signal.sent", "terminal signal delivered", signal, managed.view.Rows, managed.view.Columns)
}

func (service *TerminalService) DetachTerminal(ctx context.Context, input TerminalStreamControl) error {
	managed, err := service.authorizedManaged(ctx, input)
	if err != nil {
		if errors.Is(err, ErrTerminalNotFound) || errors.Is(err, ErrTerminalInvalidTransition) {
			return nil
		}
		return err
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	return service.recordControl(ctx, managed, input, "detached", "terminal stream detached", "", managed.view.Rows, managed.view.Columns)
}

func (service *TerminalService) FenceEpoch(
	ctx context.Context,
	projectID, sessionID string,
	sessionEpoch uint64,
	actorID, reason string,
) error {
	if service == nil {
		return ErrTerminalUnavailable
	}
	service.mu.RLock()
	managed := make([]*managedTerminal, 0)
	for _, terminal := range service.active {
		if terminal.view.ProjectID == projectID && terminal.view.SessionID == sessionID && terminal.view.SessionEpoch == sessionEpoch {
			managed = append(managed, terminal)
		}
	}
	service.mu.RUnlock()
	for _, terminal := range managed {
		_ = terminal.runtime.Close()
		terminal.cancel()
	}
	terminals, err := service.terminals.FenceEpoch(ctx, projectID, sessionID, sessionEpoch, actorID, reason)
	if err != nil {
		return err
	}
	for _, terminal := range terminals {
		service.publishLifecycle(ctx, terminal, "pty.orphaned", terminal.ID)
		service.mu.Lock()
		delete(service.active, terminal.ID)
		service.mu.Unlock()
	}
	return nil
}

func (service *TerminalService) Close() error {
	if service == nil {
		return nil
	}
	service.mu.Lock()
	service.closed = true
	active := make([]*managedTerminal, 0, len(service.active))
	for _, terminal := range service.active {
		active = append(active, terminal)
	}
	service.mu.Unlock()
	var result []error
	for _, terminal := range active {
		if err := terminal.runtime.Close(); err != nil && !errors.Is(err, context.Canceled) {
			result = append(result, err)
		}
		terminal.cancel()
	}
	return errors.Join(result...)
}

func (service *TerminalService) validateCreate(ctx context.Context, input CreateTerminalInput) error {
	if service == nil || ctx == nil || !validUUID(input.ProjectID) || !validUUID(input.SessionID) ||
		!validUUID(input.ActorID) || input.ExpectedSessionVersion == 0 || input.ExpectedSessionEpoch == 0 ||
		!validTerminalSize(input.Rows, input.Columns) {
		return ErrTerminalInvalid
	}
	directory := strings.TrimSpace(input.WorkingDirectory)
	if directory == "" || path.IsAbs(directory) || path.Clean(directory) != directory ||
		directory == ".." || strings.HasPrefix(directory, "../") || strings.Contains(directory, "\\") ||
		strings.ContainsRune(directory, 0) || len(directory) > 512 {
		return ErrTerminalInvalid
	}
	return nil
}

func (service *TerminalService) authorizedManaged(
	ctx context.Context,
	input TerminalStreamControl,
) (*managedTerminal, error) {
	if service == nil || ctx == nil || !validUUID(input.ProjectID) || !validUUID(input.SessionID) ||
		!validUUID(input.ActorID) || !validUUID(input.TerminalID) || !validUUID(input.RequestID) || input.SessionEpoch == 0 {
		return nil, ErrTerminalInvalid
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return nil, err
	}
	view := session.Snapshot()
	if view.SessionEpoch != input.SessionEpoch {
		return nil, ErrEpochFenced
	}
	if err := session.Authorize(ActionPTY, view.Version, input.SessionEpoch); err != nil {
		return nil, err
	}
	service.mu.RLock()
	managed := service.active[input.TerminalID]
	service.mu.RUnlock()
	if managed == nil {
		return nil, ErrTerminalNotFound
	}
	if managed.view.ProjectID != input.ProjectID || managed.view.SessionID != input.SessionID ||
		managed.view.SessionEpoch != input.SessionEpoch {
		return nil, ErrEpochFenced
	}
	return managed, nil
}

func (service *TerminalService) recordControl(
	ctx context.Context,
	managed *managedTerminal,
	input TerminalStreamControl,
	kind, reason, signal string,
	rows, columns uint16,
) error {
	if managed.view.State != TerminalRunning {
		return ErrTerminalInvalidTransition
	}
	transition := terminalTransition(
		managed.view, input.ActorID, input.RequestID, kind, reason,
		TerminalRunning, rows, columns, nil, "", managed.output, managed.truncated,
		managed.view.RuntimeStartedAt, nil,
	)
	transition.Signal = signal
	updated, err := service.terminals.Transition(ctx, transition)
	if err != nil {
		return err
	}
	managed.view = updated
	service.publishLifecycle(ctx, updated, "pty."+strings.ReplaceAll(kind, ".", "_"), input.RequestID)
	return nil
}

func (service *TerminalService) pump(managed *managedTerminal) {
	for value := range managed.runtime.Output() {
		service.publishOutput(managed, value)
	}
	exit, ok := <-managed.runtime.Done()
	if !ok {
		exit = RuntimeTerminalExit{ExitCode: 1, Failure: "terminal runtime ended without exit status", FinishedAt: service.now().UTC()}
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	defer managed.cancel()
	defer service.removeActive(managed.view.ID, managed)
	if managed.view.State != TerminalRunning && managed.view.State != TerminalOpening {
		return
	}
	finished := exit.FinishedAt.UTC()
	if finished.IsZero() {
		finished = service.now().UTC()
	}
	exitCode := exit.ExitCode
	state := TerminalExited
	kind := "pty.exited"
	failure := ""
	if strings.TrimSpace(exit.Failure) != "" {
		state, kind, failure = TerminalFailed, "pty.failed", boundedTerminalFailure(exit.Failure)
	}
	updated, err := service.terminals.Transition(context.Background(), terminalTransition(
		managed.view, managed.view.ActorID, managed.view.ID, "runtime.exited", "terminal runtime exited",
		state, managed.view.Rows, managed.view.Columns, &exitCode, failure,
		managed.output, managed.truncated, managed.view.RuntimeStartedAt, &finished,
	))
	if err == nil {
		managed.view = updated
		service.publishLifecycle(context.Background(), updated, kind, updated.ID)
	}
}

func (service *TerminalService) publishOutput(managed *managedTerminal, value []byte) {
	if len(value) == 0 {
		return
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if managed.view.State != TerminalRunning || managed.truncated {
		return
	}
	remaining := managed.view.OutputLimitBytes - managed.output
	if remaining <= 0 {
		managed.truncated = true
		return
	}
	if int64(len(value)) > remaining {
		value = value[:remaining]
		managed.truncated = true
	}
	redacted := redactTerminalOutput(value)
	if len(redacted) == 0 {
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"terminalId":  managed.view.ID,
		"valueBase64": base64.RawStdEncoding.EncodeToString(redacted),
	})
	if _, err := service.events.Publish(context.Background(), StreamEventInput{
		SessionID: managed.view.SessionID, SessionEpoch: managed.view.SessionEpoch,
		Channel: ChannelPTY, EventType: "pty.output", AggregateVersion: managed.view.Version,
		RequestID: managed.view.ID, Payload: payload,
	}); err != nil {
		managed.truncated = true
		_ = managed.runtime.Close()
		return
	}
	managed.output += int64(len(redacted))
}

func (service *TerminalService) failOpen(
	ctx context.Context,
	terminal TerminalView,
	actorID string,
	cause error,
) TerminalView {
	now := service.now().UTC()
	exitCode := 1
	failure := boundedTerminalFailure(cause.Error())
	failed, err := service.terminals.Transition(ctx, terminalTransition(
		terminal, actorID, terminal.ID, "runtime.failed", "fixed non-root terminal failed to open",
		TerminalFailed, terminal.Rows, terminal.Columns, &exitCode, failure, 0, false, &now, &now,
	))
	if err != nil {
		return terminal
	}
	return failed
}

func (service *TerminalService) publishLifecycle(ctx context.Context, terminal TerminalView, eventType, requestID string) {
	payload, _ := json.Marshal(terminal)
	_, _ = service.events.Publish(ctx, StreamEventInput{
		SessionID: terminal.SessionID, SessionEpoch: terminal.SessionEpoch,
		Channel: ChannelPTY, EventType: eventType, AggregateVersion: terminal.Version,
		RequestID: requestID, Payload: payload,
	})
}

func (service *TerminalService) removeActive(id string, expected *managedTerminal) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active[id] == expected {
		delete(service.active, id)
	}
}

func terminalTransition(
	terminal TerminalView,
	actorID, requestID, eventKind, reason string,
	state TerminalState,
	rows, columns uint16,
	exitCode *int,
	failure string,
	outputBytes int64,
	outputTruncated bool,
	startedAt, finishedAt *time.Time,
) TerminalTransitionInput {
	return TerminalTransitionInput{
		ProjectID: terminal.ProjectID, SessionID: terminal.SessionID, TerminalID: terminal.ID,
		SessionEpoch: terminal.SessionEpoch, ExpectedVersion: terminal.Version,
		ActorID: actorID, RequestID: requestID, EventKind: eventKind, Reason: reason,
		State: state, Rows: rows, Columns: columns, ExitCode: exitCode, Failure: failure,
		OutputBytes: outputBytes, OutputTruncated: outputTruncated,
		RuntimeStartedAt: startedAt, FinishedAt: finishedAt,
	}
}

func redactTerminalOutput(value []byte) []byte {
	redacted := terminalPrivateKeyPattern.ReplaceAll(value, []byte("[REDACTED PRIVATE KEY]"))
	redacted = terminalTokenPattern.ReplaceAll(redacted, []byte("[REDACTED TOKEN]"))
	redacted = terminalCredentialPattern.ReplaceAll(redacted, []byte("$1=[REDACTED]"))
	return append([]byte(nil), redacted...)
}

type runtimeEpochFencerGroup struct {
	fencers []RuntimeEpochFencer
}

func NewRuntimeEpochFencerGroup(fencers ...RuntimeEpochFencer) (RuntimeEpochFencer, error) {
	if len(fencers) == 0 {
		return nil, nil
	}
	result := &runtimeEpochFencerGroup{fencers: make([]RuntimeEpochFencer, 0, len(fencers))}
	for _, fencer := range fencers {
		if fencer == nil {
			return nil, errors.New("sandbox runtime resource fencer is nil")
		}
		result.fencers = append(result.fencers, fencer)
	}
	return result, nil
}

func (group *runtimeEpochFencerGroup) FenceEpoch(
	ctx context.Context,
	projectID, sessionID string,
	sessionEpoch uint64,
	actorID, reason string,
) error {
	for _, fencer := range group.fencers {
		if err := fencer.FenceEpoch(ctx, projectID, sessionID, sessionEpoch, actorID, reason); err != nil {
			return err
		}
	}
	return nil
}

var _ TerminalStreamController = (*TerminalService)(nil)
var _ RuntimeEpochFencer = (*TerminalService)(nil)
