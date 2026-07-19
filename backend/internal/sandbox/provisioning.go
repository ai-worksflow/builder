package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

var ErrProvisioningUnavailable = errors.New("sandbox provisioning is not configured")

type SessionCreator interface {
	Create(context.Context, NewSessionInput, time.Time) (SandboxSession, error)
}

type SessionConfiguration struct {
	Services []AllowedService
	Ports    []AllowedPort
}

type SessionConfigurationResolver interface {
	ResolveSessionConfiguration(context.Context, repository.CandidateWorkspace) (SessionConfiguration, error)
}

type ProvisioningPolicy struct {
	RunnerImageDigest string
	Quota             Quota
	TTL               TTLPolicy
}

type ProvisioningService struct {
	sessions   SessionCreator
	candidates CandidateControls
	resolver   SessionConfigurationResolver
	access     ProjectAuthorizer
	policy     ProvisioningPolicy
	bootstrap  RuntimeBootstrapper
	now        func() time.Time
	newID      func() string
}

func NewProvisioningService(
	sessions SessionCreator,
	candidates CandidateControls,
	resolver SessionConfigurationResolver,
	access ProjectAuthorizer,
	policy ProvisioningPolicy,
	now func() time.Time,
	bootstrap ...RuntimeBootstrapper,
) (*ProvisioningService, error) {
	return newProvisioningService(sessions, candidates, resolver, access, policy, now, uuid.NewString, bootstrap...)
}

func newProvisioningService(
	sessions SessionCreator,
	candidates CandidateControls,
	resolver SessionConfigurationResolver,
	access ProjectAuthorizer,
	policy ProvisioningPolicy,
	now func() time.Time,
	newID func() string,
	bootstrap ...RuntimeBootstrapper,
) (*ProvisioningService, error) {
	if sessions == nil || candidates == nil || resolver == nil || access == nil || now == nil || newID == nil {
		return nil, errors.New("sandbox provisioning stores, resolver, access, clock, and ID source are required")
	}
	if !validDigest(policy.RunnerImageDigest) {
		return nil, fmt.Errorf("%w: runner image digest must be exact", ErrInvalidSession)
	}
	if err := policy.Quota.validate(); err != nil {
		return nil, err
	}
	if err := policy.TTL.validate(); err != nil {
		return nil, err
	}
	if len(bootstrap) > 1 || (len(bootstrap) == 1 && bootstrap[0] == nil) {
		return nil, errors.New("at most one non-nil sandbox runtime bootstrapper is allowed")
	}
	var runtimeBootstrap RuntimeBootstrapper
	if len(bootstrap) == 1 {
		runtimeBootstrap = bootstrap[0]
	}
	return &ProvisioningService{
		sessions: sessions, candidates: candidates, resolver: resolver, access: access,
		policy: policy, bootstrap: runtimeBootstrap, now: now, newID: newID,
	}, nil
}

type CreateSessionInput struct {
	ProjectID   string
	CandidateID string
	ActorID     string
}

func (service *ProvisioningService) CreateSession(
	ctx context.Context,
	input CreateSessionInput,
) (SessionView, error) {
	if service == nil {
		return SessionView{}, ErrProvisioningUnavailable
	}
	if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
		return SessionView{}, fmt.Errorf("authorize sandbox creation: %w", err)
	}
	record, err := service.candidates.Get(ctx, input.ProjectID, input.CandidateID)
	if err != nil {
		return SessionView{}, err
	}
	if record.Candidate.Status != repository.CandidateActive || record.Candidate.Stale ||
		record.Candidate.RebaseRequired || record.Candidate.Conflicted {
		return SessionView{}, fmt.Errorf("%w: Candidate must be active and conflict-free", ErrActionBlocked)
	}
	configuration, err := service.resolver.ResolveSessionConfiguration(ctx, record.Candidate)
	if err != nil {
		return SessionView{}, fmt.Errorf("resolve exact sandbox configuration: %w", err)
	}
	sessionID := service.newID()
	if !validUUID(sessionID) {
		return SessionView{}, fmt.Errorf("%w: generated session ID", ErrInvalidSession)
	}
	now := service.now().UTC()
	if now.IsZero() || now.Before(record.Candidate.UpdatedAt) {
		return SessionView{}, fmt.Errorf("%w: provisioning timestamp", ErrInvalidSession)
	}
	created, err := service.sessions.Create(ctx, NewSessionInput{
		ID: sessionID, ActorID: input.ActorID, Candidate: record.Candidate,
		RunnerImageDigest: service.policy.RunnerImageDigest,
		Quota:             service.policy.Quota, TTL: service.policy.TTL,
		Services: configuration.Services, Ports: configuration.Ports,
	}, now)
	if err != nil {
		return SessionView{}, err
	}
	view := created.Snapshot()
	if service.bootstrap != nil {
		return service.bootstrap.Start(ctx, view, record.Candidate)
	}
	return view, nil
}

type PlatformService struct {
	*Facade
	provisioning *ProvisioningService
	tickets      *ConnectionTicketService
	control      *ControlService
	processes    *ProcessService
	terminals    *TerminalService
	ports        *PortService
	freeze       *CandidateFreezeService
}

type PlatformServiceOptions struct {
	Tickets  *ConnectionTicketService
	Control  *ControlService
	Process  *ProcessService
	Terminal *TerminalService
	Port     *PortService
	Freeze   *CandidateFreezeService
}

func NewPlatformService(
	facade *Facade,
	provisioning *ProvisioningService,
	ticketing ...*ConnectionTicketService,
) (*PlatformService, error) {
	var tickets *ConnectionTicketService
	if len(ticketing) > 1 {
		return nil, errors.New("at most one sandbox connection ticket service is allowed")
	}
	if len(ticketing) == 1 {
		tickets = ticketing[0]
	}
	return NewPlatformServiceWithOptions(
		facade, provisioning, PlatformServiceOptions{Tickets: tickets},
	)
}

func NewPlatformServiceWithOptions(
	facade *Facade,
	provisioning *ProvisioningService,
	options PlatformServiceOptions,
) (*PlatformService, error) {
	if facade == nil {
		return nil, errors.New("sandbox façade is required")
	}
	return &PlatformService{
		Facade: facade, provisioning: provisioning,
		tickets: options.Tickets, control: options.Control, processes: options.Process,
		terminals: options.Terminal,
		ports:     options.Port,
		freeze:    options.Freeze,
	}, nil
}

func (service *PlatformService) FreezeCandidate(
	ctx context.Context,
	input CandidateFreezeInput,
) (CandidateFreezeResult, error) {
	if service == nil || service.freeze == nil {
		return CandidateFreezeResult{}, ErrFreezeUnavailable
	}
	return service.freeze.Freeze(ctx, input)
}

func (service *PlatformService) AbandonCandidate(
	ctx context.Context,
	input CandidateAbandonInput,
) (CandidateSessionResult, error) {
	if service == nil || service.control == nil {
		return CandidateSessionResult{}, ErrControlUnavailable
	}
	return service.control.AbandonCandidate(ctx, input)
}

func (service *PlatformService) CreateConnectionTicket(
	ctx context.Context,
	input IssueConnectionTicketInput,
) (ConnectionTicketView, error) {
	if service == nil || service.tickets == nil {
		return ConnectionTicketView{}, ErrConnectionTicketUnavailable
	}
	return service.tickets.Issue(ctx, input)
}

func (service *PlatformService) CreateSession(
	ctx context.Context,
	input CreateSessionInput,
) (SessionView, error) {
	if service == nil || service.provisioning == nil {
		return SessionView{}, ErrProvisioningUnavailable
	}
	return service.provisioning.CreateSession(ctx, input)
}

func (service *PlatformService) SuspendSession(
	ctx context.Context,
	input SessionControlInput,
) (SessionView, error) {
	if service == nil || service.control == nil {
		return SessionView{}, ErrControlUnavailable
	}
	return service.control.Suspend(ctx, input)
}

func (service *PlatformService) ResumeSession(
	ctx context.Context,
	input SessionControlInput,
) (SessionView, error) {
	if service == nil || service.control == nil {
		return SessionView{}, ErrControlUnavailable
	}
	return service.control.Resume(ctx, input)
}

func (service *PlatformService) TerminateSession(
	ctx context.Context,
	input TerminateSessionInput,
) (SessionView, error) {
	if service == nil || service.control == nil {
		return SessionView{}, ErrControlUnavailable
	}
	return service.control.Terminate(ctx, input)
}

func (service *PlatformService) StartProcess(
	ctx context.Context,
	input StartProcessInput,
) (ProcessResult, error) {
	if service == nil || service.processes == nil {
		return ProcessResult{}, ErrProcessUnavailable
	}
	return service.processes.Start(ctx, input)
}

func (service *PlatformService) GetProcess(
	ctx context.Context,
	projectID, sessionID, processID, actorID string,
) (ProcessResult, error) {
	if service == nil || service.processes == nil {
		return ProcessResult{}, ErrProcessUnavailable
	}
	return service.processes.Get(ctx, projectID, sessionID, processID, actorID)
}

func (service *PlatformService) ListProcesses(
	ctx context.Context,
	projectID, sessionID, actorID string,
	limit int,
) (ProcessList, error) {
	if service == nil || service.processes == nil {
		return ProcessList{}, ErrProcessUnavailable
	}
	return service.processes.List(ctx, projectID, sessionID, actorID, limit)
}

func (service *PlatformService) SignalProcess(
	ctx context.Context,
	input SignalProcessInput,
) (ProcessResult, error) {
	if service == nil || service.processes == nil {
		return ProcessResult{}, ErrProcessUnavailable
	}
	return service.processes.Signal(ctx, input)
}

func (service *PlatformService) ProcessLogs(
	ctx context.Context,
	projectID, sessionID, processID, actorID string,
	offset, limit int64,
) (ProcessLogResult, error) {
	if service == nil || service.processes == nil {
		return ProcessLogResult{}, ErrProcessUnavailable
	}
	return service.processes.Logs(ctx, projectID, sessionID, processID, actorID, offset, limit)
}

func (service *PlatformService) CreateTerminal(
	ctx context.Context,
	input CreateTerminalInput,
) (TerminalResult, error) {
	if service == nil || service.terminals == nil {
		return TerminalResult{}, ErrTerminalUnavailable
	}
	return service.terminals.Create(ctx, input)
}

func (service *PlatformService) GetTerminal(
	ctx context.Context,
	projectID, sessionID, terminalID, actorID string,
) (TerminalResult, error) {
	if service == nil || service.terminals == nil {
		return TerminalResult{}, ErrTerminalUnavailable
	}
	return service.terminals.Get(ctx, projectID, sessionID, terminalID, actorID)
}

func (service *PlatformService) ListTerminals(
	ctx context.Context,
	projectID, sessionID, actorID string,
	limit int,
) (TerminalList, error) {
	if service == nil || service.terminals == nil {
		return TerminalList{}, ErrTerminalUnavailable
	}
	return service.terminals.List(ctx, projectID, sessionID, actorID, limit)
}

func (service *PlatformService) ListPorts(
	ctx context.Context,
	projectID, sessionID, actorID string,
) (PortList, error) {
	if service == nil || service.ports == nil {
		return PortList{}, ErrPortUnavailable
	}
	return service.ports.List(ctx, projectID, sessionID, actorID)
}

func (service *PlatformService) CreatePreviewLink(
	ctx context.Context,
	input IssuePreviewInput,
) (PreviewLink, error) {
	if service == nil || service.ports == nil {
		return PreviewLink{}, ErrPortUnavailable
	}
	return service.ports.Issue(ctx, input)
}

var _ SessionCreator = (*Store)(nil)
