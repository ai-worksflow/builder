package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

const SandboxLifecycleWorkerActorID = "00000000-0000-4000-8000-000000000047"

var ErrDeadlineWorkerInvalid = errors.New("sandbox lifecycle deadline worker configuration is invalid")

type deadlineSessionStore interface {
	Get(context.Context, string, string) (SandboxSession, error)
	SyncCandidate(context.Context, string, string, uint64, uint64, string) (SandboxSession, error)
	AttachCheckpoint(context.Context, string, string, uint64, uint64, string, string) (SandboxSession, error)
}

type deadlineCandidateStore interface {
	Get(context.Context, string, string) (repository.CandidateMutationRecord, error)
}

type deadlineController interface {
	suspendDeadline(context.Context, SessionControlInput) (SessionView, error)
	terminateDeadline(context.Context, TerminateSessionInput) (SessionView, error)
	completeAbandonDeadline(context.Context, SessionControlInput) (SessionView, error)
}

type DeadlineWorkerConfig struct {
	WorkerID      string
	LeaseDuration time.Duration
	RetryDelay    time.Duration
}

type DeadlineWorker struct {
	leases     DeadlineLeaseStore
	sessions   deadlineSessionStore
	candidates deadlineCandidateStore
	control    deadlineController
	config     DeadlineWorkerConfig
	newID      func() string
}

func NewDeadlineWorker(
	leases DeadlineLeaseStore,
	sessions deadlineSessionStore,
	candidates deadlineCandidateStore,
	control deadlineController,
	config DeadlineWorkerConfig,
) (*DeadlineWorker, error) {
	return newDeadlineWorker(leases, sessions, candidates, control, config, uuid.NewString)
}

func newDeadlineWorker(
	leases DeadlineLeaseStore,
	sessions deadlineSessionStore,
	candidates deadlineCandidateStore,
	control deadlineController,
	config DeadlineWorkerConfig,
	newID func() string,
) (*DeadlineWorker, error) {
	config.WorkerID = strings.TrimSpace(config.WorkerID)
	if leases == nil || sessions == nil || candidates == nil || control == nil || newID == nil ||
		config.WorkerID == "" || len(config.WorkerID) > 200 ||
		strings.ContainsAny(config.WorkerID, "\r\n\x00") ||
		config.LeaseDuration < time.Second || config.LeaseDuration > time.Hour ||
		config.RetryDelay < time.Second || config.RetryDelay > time.Hour {
		return nil, ErrDeadlineWorkerInvalid
	}
	return &DeadlineWorker{
		leases: leases, sessions: sessions, candidates: candidates, control: control,
		config: config, newID: newID,
	}, nil
}

func (worker *DeadlineWorker) RunOnce(ctx context.Context) (bool, error) {
	if worker == nil || ctx == nil {
		return false, ErrDeadlineWorkerInvalid
	}
	lease, err := worker.leases.ClaimDueDeadline(ctx, worker.config.WorkerID, worker.config.LeaseDuration)
	if err != nil || lease == nil {
		return false, err
	}
	if err := worker.process(ctx, *lease); err != nil {
		retryErr := worker.leases.RetryDeadline(ctx, *lease, worker.config.RetryDelay, err.Error())
		return true, errors.Join(err, retryErr)
	}
	if err := worker.leases.CompleteDeadline(ctx, *lease); err != nil {
		return true, err
	}
	return true, nil
}

func (worker *DeadlineWorker) process(ctx context.Context, lease DeadlineLease) error {
	session, err := worker.sessions.Get(ctx, lease.ProjectID, lease.SessionID)
	if err != nil {
		return err
	}
	view := session.Snapshot()
	action, due := deadlineActionAt(view, lease.ObservedAt)
	if !due {
		return nil
	}
	if action != lease.Action {
		return fmt.Errorf("%w: claimed %s but exact Session now requires %s", ErrDeadlineLeaseLost, lease.Action, action)
	}
	// Absolute TTL owns only the disposable Sandbox runtime boundary. The
	// Candidate tree is durable independently of a SandboxSession, so expiry
	// must not acquire an editor lease, create a checkpoint, or wait for a
	// Candidate projection reconciliation. Doing so would couple resource
	// cleanup to an expired user writer lease and could retry forever.
	if action == DeadlineTerminate {
		_, err = worker.control.terminateDeadline(ctx, TerminateSessionInput{
			SessionControlInput: SessionControlInput{
				ProjectID: view.ProjectID, SessionID: view.ID, ActorID: SandboxLifecycleWorkerActorID,
				ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
			},
			Reason: "SandboxSession absolute TTL elapsed; the durable Candidate is retained independently",
		})
		return err
	}

	record, err := worker.candidates.Get(ctx, view.ProjectID, view.Candidate.ID)
	if err != nil {
		return err
	}
	if !candidateProjectionMatches(view.Candidate, record.Candidate) {
		if view.State != StateReady {
			return ErrSessionProjectionStale
		}
		// A concurrently committed Candidate mutation is activity. Reconcile it
		// under CAS, then let the newly extended idle deadline win this cycle.
		_, syncErr := worker.sessions.SyncCandidate(
			ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
			SandboxLifecycleWorkerActorID,
		)
		if syncErr != nil {
			return syncErr
		}
		return nil
	}

	control := SessionControlInput{
		ProjectID: view.ProjectID, SessionID: view.ID, ActorID: SandboxLifecycleWorkerActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
	}
	if action == DeadlineAbandonCleanup {
		_, err = worker.control.completeAbandonDeadline(ctx, control)
		return err
	}
	if action == DeadlineSuspend {
		_, err = worker.control.suspendDeadline(ctx, control)
		return err
	}
	return fmt.Errorf("%w: unsupported lifecycle deadline action %s", ErrDeadlineWorkerInvalid, action)
}

func deadlineActionAt(view SessionView, observedAt time.Time) (DeadlineAction, bool) {
	observedAt = observedAt.UTC()
	if observedAt.IsZero() {
		return "", false
	}
	if view.State == StateTerminating && view.Candidate.Status == repository.CandidateAbandoned {
		return DeadlineAbandonCleanup, true
	}
	if (view.State == StateReady || view.State == StateSuspended || view.State == StateFailed) &&
		!view.TTL.ExpiresAt.After(observedAt) {
		return DeadlineTerminate, true
	}
	if view.State == StateReady && !view.TTL.IdleDeadline.After(observedAt) {
		if view.Candidate.Status != repository.CandidateActive {
			return DeadlineTerminate, true
		}
		return DeadlineSuspend, true
	}
	return "", false
}
