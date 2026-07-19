package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

var (
	ErrProcessUnavailable       = errors.New("sandbox process control is not configured")
	ErrProcessExists            = errors.New("sandbox process already exists")
	ErrProcessVersionConflict   = errors.New("sandbox process version conflict")
	ErrProcessInvalidTransition = errors.New("invalid sandbox process state transition")
	ErrProcessStoreIntegrity    = errors.New("sandbox process persistence integrity failure")
	ErrProcessStoreUnavailable  = errors.New("sandbox process persistence is unavailable")
)

type ProcessState string

const (
	ProcessStarting ProcessState = "starting"
	ProcessRunning  ProcessState = "running"
	ProcessExited   ProcessState = "exited"
	ProcessFailed   ProcessState = "failed"
	ProcessOrphaned ProcessState = "orphaned"
)

func (state ProcessState) String() string { return string(state) }

type ProcessView struct {
	SchemaVersion            string                    `json:"schemaVersion"`
	ID                       string                    `json:"id"`
	ProjectID                string                    `json:"projectId"`
	SessionID                string                    `json:"sessionId"`
	SessionEpoch             uint64                    `json:"sessionEpoch"`
	SessionVersionAtCreation uint64                    `json:"sessionVersionAtCreation"`
	ActorID                  string                    `json:"actorId"`
	ServiceID                string                    `json:"serviceId"`
	CommandID                string                    `json:"commandId"`
	TemplateRelease          repository.ExactReference `json:"templateRelease"`
	WorkingDirectory         string                    `json:"workingDirectory"`
	Argv                     []string                  `json:"argv"`
	LogLimitBytes            int64                     `json:"logLimitBytes"`
	State                    ProcessState              `json:"state"`
	Version                  uint64                    `json:"version"`
	PID                      int                       `json:"pid,omitempty"`
	ExitCode                 *int                      `json:"exitCode,omitempty"`
	Failure                  string                    `json:"failure,omitempty"`
	LogBytes                 int64                     `json:"logBytes"`
	LogTruncated             bool                      `json:"logTruncated"`
	RuntimeStartedAt         *time.Time                `json:"runtimeStartedAt,omitempty"`
	FinishedAt               *time.Time                `json:"finishedAt,omitempty"`
	CreatedAt                time.Time                 `json:"createdAt"`
	UpdatedAt                time.Time                 `json:"updatedAt"`
}

type ProcessRecordInput struct {
	ID                       string
	ProjectID                string
	SessionID                string
	SessionEpoch             uint64
	SessionVersionAtCreation uint64
	ActorID                  string
	Command                  ResolvedProcessCommand
	LogLimitBytes            int64
}

type ProcessObservation struct {
	ProjectID              string
	SessionID              string
	ProcessID              string
	ActorID                string
	ExpectedProcessVersion uint64
	ExpectedSessionEpoch   uint64
	EventKind              string
	Signal                 string
	Reason                 string
	Status                 RuntimeProcessStatus
}

type SandboxProcessStore interface {
	Create(context.Context, ProcessRecordInput) (ProcessView, error)
	Get(context.Context, string, string, string) (ProcessView, error)
	List(context.Context, string, string, int) ([]ProcessView, error)
	Observe(context.Context, ProcessObservation) (ProcessView, error)
	FenceEpoch(context.Context, string, string, uint64, string, string) ([]ProcessView, error)
}

type StartProcessInput struct {
	ProjectID              string
	SessionID              string
	ActorID                string
	ExpectedSessionVersion uint64
	ExpectedSessionEpoch   uint64
	ServiceID              string
	CommandID              string
}

type SignalProcessInput struct {
	ProjectID              string
	SessionID              string
	ProcessID              string
	ActorID                string
	ExpectedSessionVersion uint64
	ExpectedSessionEpoch   uint64
	ExpectedProcessVersion uint64
	Signal                 string
}

type ProcessResult struct {
	Session SessionView `json:"session"`
	Process ProcessView `json:"process"`
}

type ProcessList struct {
	Session   SessionView   `json:"session"`
	Processes []ProcessView `json:"processes"`
}

type ProcessLogResult struct {
	Session SessionView       `json:"session"`
	Process ProcessView       `json:"process"`
	Log     RuntimeProcessLog `json:"log"`
}

type ProcessControlError struct {
	Session SessionView
	Process ProcessView
	Cause   error
}

func (err *ProcessControlError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("sandbox process control failed for process %s: %v", err.Process.ID, err.Cause)
}

func (err *ProcessControlError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

type ProcessService struct {
	sessions   ControlSessionStore
	candidates CandidateControls
	commands   SessionProcessCommandResolver
	workspaces *WorkspaceMaterializer
	runtime    RuntimeProcessManager
	processes  SandboxProcessStore
	access     ProjectAuthorizer
	events     StreamEventStore
	logLimit   int64
	newID      func() string
	now        func() time.Time
}

func NewProcessService(
	sessions ControlSessionStore,
	candidates CandidateControls,
	commands SessionProcessCommandResolver,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeProcessManager,
	processes SandboxProcessStore,
	access ProjectAuthorizer,
	events StreamEventStore,
	logLimit int64,
) (*ProcessService, error) {
	return newProcessService(
		sessions, candidates, commands, workspaces, runtime, processes, access, events,
		logLimit, uuid.NewString, time.Now,
	)
}

func newProcessService(
	sessions ControlSessionStore,
	candidates CandidateControls,
	commands SessionProcessCommandResolver,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeProcessManager,
	processes SandboxProcessStore,
	access ProjectAuthorizer,
	events StreamEventStore,
	logLimit int64,
	newID func() string,
	now func() time.Time,
) (*ProcessService, error) {
	if sessions == nil || candidates == nil || commands == nil || workspaces == nil || runtime == nil ||
		processes == nil || access == nil || events == nil || newID == nil || now == nil ||
		logLimit < 1 || logLimit > 64<<20 {
		return nil, errors.New("sandbox process stores, exact resolver, runtime, access, events, and bounded log policy are required")
	}
	return &ProcessService{
		sessions: sessions, candidates: candidates, commands: commands, workspaces: workspaces,
		runtime: runtime, processes: processes, access: access, events: events,
		logLimit: logLimit, newID: newID, now: now,
	}, nil
}

func (service *ProcessService) Start(ctx context.Context, input StartProcessInput) (ProcessResult, error) {
	if err := service.validateStart(ctx, input); err != nil {
		return ProcessResult{}, err
	}
	if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
		return ProcessResult{}, fmt.Errorf("authorize sandbox process start: %w", err)
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := session.Authorize(ActionProcess, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
		return ProcessResult{}, err
	}
	view := session.Snapshot()
	command, err := service.commands.ResolveProcessCommand(ctx, view, input.ServiceID, input.CommandID)
	if err != nil {
		return ProcessResult{}, err
	}
	record, err := service.candidates.Get(ctx, view.ProjectID, view.Candidate.ID)
	if err != nil {
		return ProcessResult{}, err
	}
	if !candidateProjectionMatches(view.Candidate, record.Candidate) {
		return ProcessResult{}, ErrSessionProjectionStale
	}
	mount, err := service.workspaces.Materialize(ctx, view, record.Candidate)
	if err != nil {
		return ProcessResult{}, err
	}
	runtimeSpec, err := RuntimeSpecForSession(view, mount)
	if err != nil {
		return ProcessResult{}, err
	}
	processID := service.newID()
	if !validUUID(processID) {
		return ProcessResult{}, ErrProcessInvalid
	}
	process, err := service.processes.Create(ctx, ProcessRecordInput{
		ID: processID, ProjectID: view.ProjectID, SessionID: view.ID,
		SessionEpoch: view.SessionEpoch, SessionVersionAtCreation: view.Version,
		ActorID: input.ActorID, Command: command, LogLimitBytes: service.logLimit,
	})
	if err != nil {
		return ProcessResult{}, err
	}
	runtimeProcess := runtimeProcessSpec(process, runtimeSpec)
	status, err := service.runtime.StartProcess(ctx, runtimeProcess)
	if err != nil {
		failed := service.failStart(ctx, process, input.ActorID, err)
		service.publish(ctx, failed, "process.failed")
		return ProcessResult{Session: view, Process: failed}, &ProcessControlError{
			Session: view, Process: failed, Cause: err,
		}
	}
	if status.State != ProcessStarting.String() {
		process, err = service.processes.Observe(ctx, ProcessObservation{
			ProjectID: view.ProjectID, SessionID: view.ID, ProcessID: process.ID,
			ActorID: input.ActorID, ExpectedProcessVersion: process.Version,
			ExpectedSessionEpoch: view.SessionEpoch, EventKind: "runtime.observed",
			Reason: "sandbox supervisor reported process " + status.State, Status: status,
		})
		if err != nil {
			return ProcessResult{Session: view, Process: process}, &ProcessControlError{
				Session: view, Process: process, Cause: err,
			}
		}
	}
	service.publish(ctx, process, processEventType(process.State))
	return ProcessResult{Session: view, Process: process}, nil
}

func (service *ProcessService) Get(
	ctx context.Context,
	projectID, sessionID, processID, actorID string,
) (ProcessResult, error) {
	if service == nil || ctx == nil || !validUUID(projectID) || !validUUID(sessionID) ||
		!validUUID(processID) || !validUUID(actorID) {
		return ProcessResult{}, ErrProcessInvalid
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return ProcessResult{}, fmt.Errorf("authorize sandbox process view: %w", err)
	}
	session, err := service.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return ProcessResult{}, err
	}
	view := session.Snapshot()
	process, err := service.processes.Get(ctx, projectID, sessionID, processID)
	if err != nil {
		return ProcessResult{}, err
	}
	if process.SessionEpoch == view.SessionEpoch && processActive(process.State) && view.State == StateReady {
		process = service.refresh(ctx, view, process, actorID)
	}
	return ProcessResult{Session: view, Process: process}, nil
}

func (service *ProcessService) List(
	ctx context.Context,
	projectID, sessionID, actorID string,
	limit int,
) (ProcessList, error) {
	if service == nil || ctx == nil || !validUUID(projectID) || !validUUID(sessionID) || !validUUID(actorID) {
		return ProcessList{}, ErrProcessInvalid
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return ProcessList{}, fmt.Errorf("authorize sandbox process list: %w", err)
	}
	session, err := service.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return ProcessList{}, err
	}
	processes, err := service.processes.List(ctx, projectID, sessionID, limit)
	if err != nil {
		return ProcessList{}, err
	}
	return ProcessList{Session: session.Snapshot(), Processes: processes}, nil
}

func (service *ProcessService) Signal(ctx context.Context, input SignalProcessInput) (ProcessResult, error) {
	if err := service.validateSignal(ctx, input); err != nil {
		return ProcessResult{}, err
	}
	if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
		return ProcessResult{}, fmt.Errorf("authorize sandbox process signal: %w", err)
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := session.Authorize(ActionProcess, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
		return ProcessResult{}, err
	}
	view := session.Snapshot()
	process, err := service.processes.Get(ctx, input.ProjectID, input.SessionID, input.ProcessID)
	if err != nil {
		return ProcessResult{}, err
	}
	if process.SessionEpoch != view.SessionEpoch {
		return ProcessResult{}, ErrEpochFenced
	}
	if process.Version != input.ExpectedProcessVersion {
		return ProcessResult{}, ErrProcessVersionConflict
	}
	runtimeSpec, err := service.runtimeSpecForStored(view, process)
	if err != nil {
		return ProcessResult{}, err
	}
	status, err := service.runtime.SignalProcess(ctx, runtimeSpec, input.Signal)
	if err != nil {
		return ProcessResult{Session: view, Process: process}, &ProcessControlError{
			Session: view, Process: process, Cause: err,
		}
	}
	updated, err := service.processes.Observe(ctx, ProcessObservation{
		ProjectID: view.ProjectID, SessionID: view.ID, ProcessID: process.ID,
		ActorID: input.ActorID, ExpectedProcessVersion: process.Version,
		ExpectedSessionEpoch: view.SessionEpoch, EventKind: "signal.sent",
		Signal: strings.ToUpper(strings.TrimSpace(input.Signal)),
		Reason: "signal delivered to supervised process group", Status: status,
	})
	if err != nil {
		return ProcessResult{Session: view, Process: process}, &ProcessControlError{
			Session: view, Process: process, Cause: err,
		}
	}
	service.publish(ctx, updated, "process.signal")
	return ProcessResult{Session: view, Process: updated}, nil
}

func (service *ProcessService) Logs(
	ctx context.Context,
	projectID, sessionID, processID, actorID string,
	offset, limit int64,
) (ProcessLogResult, error) {
	if service == nil || ctx == nil || !validUUID(projectID) || !validUUID(sessionID) ||
		!validUUID(processID) || !validUUID(actorID) || offset < 0 || limit < 1 || limit > 1<<20 {
		return ProcessLogResult{}, ErrProcessInvalid
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return ProcessLogResult{}, fmt.Errorf("authorize sandbox process logs: %w", err)
	}
	session, err := service.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return ProcessLogResult{}, err
	}
	view := session.Snapshot()
	process, err := service.processes.Get(ctx, projectID, sessionID, processID)
	if err != nil {
		return ProcessLogResult{}, err
	}
	if process.SessionEpoch != view.SessionEpoch {
		return ProcessLogResult{}, ErrEpochFenced
	}
	runtimeSpec, err := service.runtimeSpecForStored(view, process)
	if err != nil {
		return ProcessLogResult{}, err
	}
	log, err := service.runtime.ReadProcessLog(ctx, runtimeSpec, offset, limit)
	if err != nil {
		return ProcessLogResult{}, err
	}
	return ProcessLogResult{Session: view, Process: process, Log: log}, nil
}

// FenceEpoch is invoked only after the old runtime has been stopped. It marks
// every non-final process in that exact epoch as orphaned before a new session
// epoch can become usable.
func (service *ProcessService) FenceEpoch(
	ctx context.Context,
	projectID, sessionID string,
	sessionEpoch uint64,
	actorID, reason string,
) error {
	if service == nil {
		return ErrProcessUnavailable
	}
	processes, err := service.processes.FenceEpoch(ctx, projectID, sessionID, sessionEpoch, actorID, reason)
	if err != nil {
		return err
	}
	for _, process := range processes {
		service.publish(ctx, process, "process.orphaned")
	}
	return nil
}

func (service *ProcessService) refresh(
	ctx context.Context,
	session SessionView,
	process ProcessView,
	actorID string,
) ProcessView {
	spec, err := service.runtimeSpecForStored(session, process)
	if err != nil {
		return process
	}
	status, err := service.runtime.InspectProcess(ctx, spec)
	if err != nil || status.State == ProcessStarting.String() || runtimeStatusMatchesProcess(status, process) {
		return process
	}
	updated, err := service.processes.Observe(ctx, ProcessObservation{
		ProjectID: session.ProjectID, SessionID: session.ID, ProcessID: process.ID,
		ActorID: actorID, ExpectedProcessVersion: process.Version,
		ExpectedSessionEpoch: session.SessionEpoch, EventKind: "runtime.observed",
		Reason: "sandbox supervisor status refresh", Status: status,
	})
	if err != nil {
		return process
	}
	service.publish(ctx, updated, processEventType(updated.State))
	return updated
}

func (service *ProcessService) runtimeSpecForStored(
	session SessionView,
	process ProcessView,
) (RuntimeProcessSpec, error) {
	if process.ProjectID != session.ProjectID || process.SessionID != session.ID ||
		process.SessionEpoch != session.SessionEpoch {
		return RuntimeProcessSpec{}, ErrEpochFenced
	}
	mount, exists, err := service.workspaces.ExistingMount(session.ID)
	if err != nil {
		return RuntimeProcessSpec{}, err
	}
	if !exists {
		return RuntimeProcessSpec{}, ErrWorkspaceConflict
	}
	runtimeSpec, err := RuntimeSpecForSession(session, mount)
	if err != nil {
		return RuntimeProcessSpec{}, err
	}
	return runtimeProcessSpec(process, runtimeSpec), nil
}

func runtimeProcessSpec(process ProcessView, runtime RuntimeSpec) RuntimeProcessSpec {
	return RuntimeProcessSpec{
		Runtime: runtime, ID: process.ID, CommandID: process.CommandID,
		WorkingDirectory: process.WorkingDirectory, Argv: append([]string(nil), process.Argv...),
		LogLimitBytes: process.LogLimitBytes,
	}
}

func (service *ProcessService) failStart(
	ctx context.Context,
	process ProcessView,
	actorID string,
	cause error,
) ProcessView {
	now := service.now().UTC()
	if now.IsZero() || now.Before(process.CreatedAt) {
		now = process.CreatedAt
	}
	failure := strings.TrimSpace(cause.Error())
	if len(failure) > 1000 {
		failure = failure[:1000]
	}
	exitCode := 1
	status := RuntimeProcessStatus{
		SchemaVersion: RuntimeProcessSchemaVersion, ID: process.ID, State: ProcessFailed.String(),
		Argv: append([]string(nil), process.Argv...), WorkingDirectory: process.WorkingDirectory,
		ExitCode: &exitCode, Failure: failure, StartedAt: now, FinishedAt: now,
	}
	updated, err := service.processes.Observe(ctx, ProcessObservation{
		ProjectID: process.ProjectID, SessionID: process.SessionID, ProcessID: process.ID,
		ActorID: actorID, ExpectedProcessVersion: process.Version,
		ExpectedSessionEpoch: process.SessionEpoch, EventKind: "start.failed",
		Reason: "sandbox process supervisor start failed", Status: status,
	})
	if err == nil {
		return updated
	}
	return process
}

func (service *ProcessService) publish(ctx context.Context, process ProcessView, eventType string) {
	payload, err := json.Marshal(process)
	if err != nil {
		return
	}
	_, _ = service.events.Publish(context.WithoutCancel(ctx), StreamEventInput{
		SessionID: process.SessionID, SessionEpoch: process.SessionEpoch,
		Channel: ChannelProcess, EventType: eventType,
		AggregateVersion: process.Version, Payload: payload,
	})
}

func (service *ProcessService) validateStart(ctx context.Context, input StartProcessInput) error {
	if service == nil {
		return ErrProcessUnavailable
	}
	if ctx == nil || !validUUID(input.ProjectID) || !validUUID(input.SessionID) || !validUUID(input.ActorID) ||
		input.ExpectedSessionVersion == 0 || input.ExpectedSessionEpoch == 0 ||
		!slugPattern.MatchString(input.ServiceID) || !slugPattern.MatchString(input.CommandID) {
		return ErrProcessInvalid
	}
	return nil
}

func (service *ProcessService) validateSignal(ctx context.Context, input SignalProcessInput) error {
	if service == nil {
		return ErrProcessUnavailable
	}
	signal := strings.ToUpper(strings.TrimSpace(input.Signal))
	if ctx == nil || !validUUID(input.ProjectID) || !validUUID(input.SessionID) ||
		!validUUID(input.ProcessID) || !validUUID(input.ActorID) ||
		input.ExpectedSessionVersion == 0 || input.ExpectedSessionEpoch == 0 ||
		input.ExpectedProcessVersion == 0 ||
		(signal != "INT" && signal != "TERM" && signal != "KILL" && signal != "HUP") {
		return ErrProcessInvalid
	}
	return nil
}

func validateProcessView(value ProcessView) error {
	if value.SchemaVersion != RuntimeProcessSchemaVersion || !validUUID(value.ID) ||
		!validUUID(value.ProjectID) || !validUUID(value.SessionID) || !validUUID(value.ActorID) ||
		value.SessionEpoch == 0 || value.SessionVersionAtCreation == 0 || value.Version == 0 ||
		!slugPattern.MatchString(value.ServiceID) || !slugPattern.MatchString(value.CommandID) ||
		validateExactReference(value.TemplateRelease) != nil ||
		!validProcessWorkingDirectory(value.WorkingDirectory) ||
		value.LogLimitBytes < 1 || value.LogLimitBytes > 64<<20 ||
		value.LogBytes < 0 || value.LogBytes > value.LogLimitBytes ||
		value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) ||
		len(value.Argv) == 0 || len(value.Argv) > 64 {
		return ErrProcessStoreIntegrity
	}
	for _, argument := range value.Argv {
		if strings.TrimSpace(argument) == "" || len(argument) > 4096 || strings.ContainsAny(argument, "\x00\r\n") {
			return ErrProcessStoreIntegrity
		}
	}
	switch value.State {
	case ProcessStarting:
		if value.PID != 0 || value.ExitCode != nil || value.Failure != "" ||
			value.RuntimeStartedAt != nil || value.FinishedAt != nil {
			return ErrProcessStoreIntegrity
		}
	case ProcessRunning:
		if value.PID < 2 || value.ExitCode != nil || value.Failure != "" ||
			value.RuntimeStartedAt == nil || value.FinishedAt != nil {
			return ErrProcessStoreIntegrity
		}
	case ProcessExited:
		if value.PID < 2 || value.ExitCode == nil || value.Failure != "" ||
			value.RuntimeStartedAt == nil || value.FinishedAt == nil {
			return ErrProcessStoreIntegrity
		}
	case ProcessFailed:
		if value.ExitCode == nil || strings.TrimSpace(value.Failure) == "" ||
			value.RuntimeStartedAt == nil || value.FinishedAt == nil {
			return ErrProcessStoreIntegrity
		}
	case ProcessOrphaned:
		if value.ExitCode != nil || strings.TrimSpace(value.Failure) == "" || value.FinishedAt == nil {
			return ErrProcessStoreIntegrity
		}
	default:
		return ErrProcessStoreIntegrity
	}
	if value.RuntimeStartedAt != nil && value.RuntimeStartedAt.Before(value.CreatedAt) {
		return ErrProcessStoreIntegrity
	}
	if value.FinishedAt != nil && (value.RuntimeStartedAt == nil || value.FinishedAt.Before(*value.RuntimeStartedAt)) {
		return ErrProcessStoreIntegrity
	}
	return nil
}

func validProcessWorkingDirectory(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && !path.IsAbs(value) && path.Clean(value) == value &&
		value != "." && value != ".." && !strings.HasPrefix(value, "../") &&
		!strings.Contains(value, "\\") && !strings.ContainsAny(value, "\x00\r\n")
}

func processActive(state ProcessState) bool {
	return state == ProcessStarting || state == ProcessRunning
}

func processEventType(state ProcessState) string {
	switch state {
	case ProcessStarting:
		return "process.starting"
	case ProcessRunning:
		return "process.running"
	case ProcessExited:
		return "process.exited"
	case ProcessFailed:
		return "process.failed"
	case ProcessOrphaned:
		return "process.orphaned"
	default:
		return "process.updated"
	}
}

func runtimeStatusMatchesProcess(status RuntimeProcessStatus, process ProcessView) bool {
	if ProcessState(status.State) != process.State || status.PID != process.PID ||
		status.LogBytes != process.LogBytes || status.LogTruncated != process.LogTruncated ||
		status.Failure != process.Failure {
		return false
	}
	if (status.ExitCode == nil) != (process.ExitCode == nil) ||
		(status.ExitCode != nil && *status.ExitCode != *process.ExitCode) {
		return false
	}
	return true
}

var _ interface {
	FenceEpoch(context.Context, string, string, uint64, string, string) error
} = (*ProcessService)(nil)
