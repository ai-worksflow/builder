package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type WorkerStore interface {
	GetAttempt(context.Context, string, string) (AgentAttempt, error)
	Claim(context.Context, string, uint64, string, string, time.Duration) (AgentAttempt, error)
	Renew(context.Context, string, uint64, uint64, string, string, time.Duration) (AgentAttempt, error)
	Advance(context.Context, string, AdvanceAttemptInput) (AgentAttempt, error)
	MarkStale(context.Context, string, uint64, string, string) (AgentAttempt, error)
}

type WorkerPrincipal struct {
	ActorID  string
	WorkerID string
}

type WorkerCapabilityAuthorizer interface {
	AuthorizeAgentWorker(context.Context, WorkerPrincipal, AgentAttempt, string) error
}

type WorkerAdvanceInput struct {
	ProjectID          string
	AttemptID          string
	ExpectedVersion    uint64
	ExpectedFenceEpoch uint64
	Target             AttemptState
	Reason             string
	Evidence           AttemptEvidence
	ExitReason         string
}

// WorkerService is deliberately separate from ControlService. Only a trusted
// platform Runner adapter holding a task-scoped capability can claim a lease
// or publish platform-collected evidence; no browser handler receives this
// interface.
type WorkerService struct {
	store  WorkerStore
	access WorkerCapabilityAuthorizer
}

func NewWorkerService(store WorkerStore, access WorkerCapabilityAuthorizer) (*WorkerService, error) {
	if store == nil || access == nil {
		return nil, errors.New("agent worker store and capability authorizer are required")
	}
	return &WorkerService{store: store, access: access}, nil
}

func (service *WorkerService) Claim(
	ctx context.Context,
	principal WorkerPrincipal,
	projectID, attemptID string,
	expectedVersion uint64,
	ttl time.Duration,
) (AgentAttempt, error) {
	attempt, principal, err := service.authorize(ctx, principal, projectID, attemptID, "claim")
	if err != nil {
		return AgentAttempt{}, err
	}
	return service.store.Claim(
		ctx, attempt.ID, expectedVersion, principal.ActorID, principal.WorkerID, ttl,
	)
}

func (service *WorkerService) Renew(
	ctx context.Context,
	principal WorkerPrincipal,
	projectID, attemptID string,
	expectedVersion, expectedFenceEpoch uint64,
	ttl time.Duration,
) (AgentAttempt, error) {
	attempt, principal, err := service.authorize(ctx, principal, projectID, attemptID, "renew")
	if err != nil {
		return AgentAttempt{}, err
	}
	return service.store.Renew(
		ctx, attempt.ID, expectedVersion, expectedFenceEpoch,
		principal.ActorID, principal.WorkerID, ttl,
	)
}

func (service *WorkerService) Advance(
	ctx context.Context,
	principal WorkerPrincipal,
	input WorkerAdvanceInput,
) (AgentAttempt, error) {
	attempt, principal, err := service.authorize(
		ctx, principal, input.ProjectID, input.AttemptID, "advance",
	)
	if err != nil {
		return AgentAttempt{}, err
	}
	return service.store.Advance(ctx, attempt.ID, AdvanceAttemptInput{
		ExpectedVersion: input.ExpectedVersion, ExpectedFenceEpoch: input.ExpectedFenceEpoch,
		ActorID: principal.ActorID, WorkerID: principal.WorkerID,
		Target: input.Target, Reason: input.Reason, Evidence: input.Evidence,
		ExitReason: input.ExitReason,
	})
}

func (service *WorkerService) MarkStale(
	ctx context.Context,
	principal WorkerPrincipal,
	projectID, attemptID string,
	expectedVersion uint64,
	reason string,
) (AgentAttempt, error) {
	attempt, principal, err := service.authorize(ctx, principal, projectID, attemptID, "mark_stale")
	if err != nil {
		return AgentAttempt{}, err
	}
	return service.store.MarkStale(ctx, attempt.ID, expectedVersion, principal.ActorID, reason)
}

func (service *WorkerService) authorize(
	ctx context.Context,
	principal WorkerPrincipal,
	projectID, attemptID, action string,
) (AgentAttempt, WorkerPrincipal, error) {
	if !validUUIDs(projectID, attemptID, principal.ActorID) {
		return AgentAttempt{}, WorkerPrincipal{}, fmt.Errorf("%w: worker project, Attempt, or actor", ErrInvalidAttempt)
	}
	workerID, err := normalizeStableValue(principal.WorkerID, 160)
	if err != nil || workerID != principal.WorkerID {
		return AgentAttempt{}, WorkerPrincipal{}, fmt.Errorf("%w: worker identity", ErrInvalidAttempt)
	}
	attempt, err := service.store.GetAttempt(ctx, projectID, attemptID)
	if err != nil {
		return AgentAttempt{}, WorkerPrincipal{}, err
	}
	if err := service.access.AuthorizeAgentWorker(ctx, principal, attempt, action); err != nil {
		return AgentAttempt{}, WorkerPrincipal{}, fmt.Errorf("authorize Agent worker %s: %w", action, err)
	}
	return attempt, principal, nil
}
