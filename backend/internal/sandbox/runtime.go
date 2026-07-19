package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

var (
	ErrRuntimeInvalid     = errors.New("invalid sandbox runtime request")
	ErrRuntimeUnavailable = errors.New("sandbox runtime is unavailable")
	ErrRuntimeConflict    = errors.New("sandbox runtime identity conflicts with the exact session")
	ErrRuntimeNotReady    = errors.New("sandbox runtime did not become ready")
	ErrWorkspaceInvalid   = errors.New("invalid sandbox workspace projection")
	ErrWorkspaceConflict  = errors.New("sandbox workspace projection conflicts with the Candidate")
)

type WorkspaceMount struct {
	SessionRoot string
	Workspace   string
	CodexHome   string
}

type RuntimeSpec struct {
	ProjectID         string
	SessionID         string
	SessionEpoch      uint64
	RunnerImageDigest string
	Workspace         WorkspaceMount
	Quota             Quota
	Ports             []AllowedPort
}

type RuntimeStatus struct {
	ID           string            `json:"id"`
	SessionID    string            `json:"sessionId"`
	SessionEpoch uint64            `json:"sessionEpoch"`
	State        string            `json:"state"`
	Healthy      bool              `json:"healthy"`
	HostPorts    map[string]int    `json:"hostPorts"`
	Labels       map[string]string `json:"labels"`
}

type RuntimeManager interface {
	Ensure(context.Context, RuntimeSpec) (RuntimeStatus, error)
	Start(context.Context, RuntimeSpec) (RuntimeStatus, error)
	WaitReady(context.Context, RuntimeSpec) (RuntimeStatus, error)
	Suspend(context.Context, RuntimeSpec) (RuntimeStatus, error)
	Resume(context.Context, RuntimeSpec) (RuntimeStatus, error)
	Terminate(context.Context, RuntimeSpec) error
	Inspect(context.Context, RuntimeSpec) (RuntimeStatus, error)
}

func RuntimeSpecForSession(view SessionView, workspace WorkspaceMount) (RuntimeSpec, error) {
	if view.SchemaVersion != SessionSchemaVersion || !validUUID(view.ProjectID) || !validUUID(view.ID) ||
		view.SessionEpoch == 0 || !validDigest(view.RunnerImageDigest) ||
		strings.TrimSpace(workspace.SessionRoot) == "" || strings.TrimSpace(workspace.Workspace) == "" ||
		strings.TrimSpace(workspace.CodexHome) == "" {
		return RuntimeSpec{}, ErrRuntimeInvalid
	}
	if err := view.Quota.validate(); err != nil {
		return RuntimeSpec{}, err
	}
	ports, err := normalizePorts(view.AllowedPorts, view.AllowedServices, view.Quota.PreviewPortLimit)
	if err != nil {
		return RuntimeSpec{}, err
	}
	return RuntimeSpec{
		ProjectID: view.ProjectID, SessionID: view.ID, SessionEpoch: view.SessionEpoch,
		RunnerImageDigest: view.RunnerImageDigest, Workspace: workspace, Quota: view.Quota,
		Ports: ports,
	}, nil
}

func validateRuntimeSpec(spec RuntimeSpec) error {
	if !validUUID(spec.ProjectID) || !validUUID(spec.SessionID) || spec.SessionEpoch == 0 ||
		!validDigest(spec.RunnerImageDigest) || strings.TrimSpace(spec.Workspace.SessionRoot) == "" ||
		strings.TrimSpace(spec.Workspace.Workspace) == "" || strings.TrimSpace(spec.Workspace.CodexHome) == "" {
		return ErrRuntimeInvalid
	}
	if err := spec.Quota.validate(); err != nil {
		return err
	}
	if len(spec.Ports) > spec.Quota.PreviewPortLimit {
		return fmt.Errorf("%w: preview port quota exceeded", ErrRuntimeInvalid)
	}
	seenNames, seenNumbers := map[string]bool{}, map[int]bool{}
	for _, port := range spec.Ports {
		if strings.TrimSpace(port.Name) == "" || strings.TrimSpace(port.ServiceID) == "" ||
			port.Number < 1 || port.Number > 65535 ||
			(port.Protocol != "http" && port.Protocol != "https" && port.Protocol != "tcp") ||
			seenNames[port.Name] || seenNumbers[port.Number] {
			return ErrRuntimeInvalid
		}
		seenNames[port.Name], seenNumbers[port.Number] = true, true
	}
	return nil
}

type RuntimeBootstrapper interface {
	Start(context.Context, SessionView, repository.CandidateWorkspace) (SessionView, error)
}

type RuntimeBootstrapError struct {
	Session SessionView
	Cause   error
}

func (err *RuntimeBootstrapError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("sandbox runtime bootstrap failed for session %s: %v", err.Session.ID, err.Cause)
}

func (err *RuntimeBootstrapError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

type LifecycleSessionStore interface {
	Get(context.Context, string, string) (SandboxSession, error)
	Transition(context.Context, string, string, uint64, uint64, State, string, string, string) (SandboxSession, error)
}

type LifecycleService struct {
	sessions   LifecycleSessionStore
	workspaces *WorkspaceMaterializer
	runtime    RuntimeManager
	events     StreamEventStore
}

func NewLifecycleService(
	sessions LifecycleSessionStore,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeManager,
	events StreamEventStore,
) (*LifecycleService, error) {
	if sessions == nil || workspaces == nil || runtime == nil || events == nil {
		return nil, errors.New("sandbox lifecycle session store, workspace, runtime, and events are required")
	}
	return &LifecycleService{sessions: sessions, workspaces: workspaces, runtime: runtime, events: events}, nil
}

func (service *LifecycleService) Start(
	ctx context.Context,
	initial SessionView,
	candidate repository.CandidateWorkspace,
) (SessionView, error) {
	if service == nil || ctx == nil || initial.State != StateProvisioning ||
		initial.Candidate.ID != candidate.ID || initial.Candidate.TreeHash != candidate.CurrentTree.TreeHash ||
		initial.Candidate.Version != candidate.Version || initial.ProjectID != candidate.ProjectID {
		return SessionView{}, ErrRuntimeInvalid
	}
	workspace, err := service.workspaces.Materialize(ctx, initial, candidate)
	if err != nil {
		return service.failBootstrap(ctx, initial, err)
	}
	spec, err := RuntimeSpecForSession(initial, workspace)
	if err != nil {
		return service.failBootstrap(ctx, initial, err)
	}
	if _, err := service.runtime.Ensure(ctx, spec); err != nil {
		return service.failRuntimeBootstrap(ctx, initial, spec, err)
	}
	starting, err := service.sessions.Transition(
		ctx, initial.ProjectID, initial.ID, initial.Version, initial.SessionEpoch,
		StateStarting, initial.ActorID, "runtime provisioned", "",
	)
	if err != nil {
		return service.failRuntimeBootstrap(ctx, initial, spec, err)
	}
	startingView := starting.Snapshot()
	service.publishState(ctx, startingView)
	if _, err := service.runtime.Start(ctx, spec); err != nil {
		return service.failRuntimeBootstrap(ctx, startingView, spec, err)
	}
	if _, err := service.runtime.WaitReady(ctx, spec); err != nil {
		return service.failRuntimeBootstrap(ctx, startingView, spec, err)
	}
	ready, err := service.sessions.Transition(
		ctx, startingView.ProjectID, startingView.ID, startingView.Version, startingView.SessionEpoch,
		StateReady, startingView.ActorID, "runtime ready", "",
	)
	if err != nil {
		return service.failRuntimeBootstrap(ctx, startingView, spec, err)
	}
	view := ready.Snapshot()
	service.publishState(ctx, view)
	return view, nil
}

func (service *LifecycleService) failRuntimeBootstrap(
	ctx context.Context,
	view SessionView,
	spec RuntimeSpec,
	cause error,
) (SessionView, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := service.runtime.Terminate(cleanupCtx, spec); err != nil {
		cause = errors.Join(cause, fmt.Errorf("clean up failed runtime: %w", err))
	}
	return service.failBootstrap(ctx, view, cause)
}

func (service *LifecycleService) failBootstrap(
	ctx context.Context,
	view SessionView,
	cause error,
) (SessionView, error) {
	failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if current, err := service.sessions.Get(failureCtx, view.ProjectID, view.ID); err == nil {
		currentView := current.Snapshot()
		if currentView.SessionEpoch != view.SessionEpoch {
			return currentView, &RuntimeBootstrapError{Session: currentView, Cause: errors.Join(cause, ErrEpochFenced)}
		}
		view = currentView
		if view.State == StateFailed || view.State == StateTerminated {
			return view, &RuntimeBootstrapError{Session: view, Cause: cause}
		}
	}
	reason := "runtime bootstrap failed"
	failed, err := service.sessions.Transition(
		failureCtx, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
		StateFailed, view.ActorID, reason, "",
	)
	if err == nil {
		view = failed.Snapshot()
		service.publishState(failureCtx, view)
	} else {
		cause = errors.Join(cause, err)
	}
	return view, &RuntimeBootstrapError{Session: view, Cause: cause}
}

func (service *LifecycleService) publishState(ctx context.Context, view SessionView) {
	publishSessionState(ctx, service.events, view)
}

func publishSessionState(ctx context.Context, events StreamEventStore, view SessionView) {
	payload, err := json.Marshal(map[string]any{
		"state": view.State, "version": view.Version, "allowedActions": view.AllowedActions,
		"blockingReasons": view.BlockingReasons,
	})
	if err != nil {
		return
	}
	_, _ = events.Publish(ctx, StreamEventInput{
		SessionID: view.ID, SessionEpoch: view.SessionEpoch, Channel: ChannelControl,
		EventType: "session.state", AggregateVersion: view.Version, Payload: payload,
	})
}

var _ RuntimeBootstrapper = (*LifecycleService)(nil)
