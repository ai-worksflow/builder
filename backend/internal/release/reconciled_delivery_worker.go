package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DeliveryAttemptKind string

const (
	DeliveryAttemptSubmit    DeliveryAttemptKind = "submit"
	DeliveryAttemptReconcile DeliveryAttemptKind = "reconcile"
	DeliveryAttemptResubmit  DeliveryAttemptKind = "resubmit"
)

type DeliveryOperationAttempt struct {
	OperationID string
	Ordinal     uint64
	Kind        DeliveryAttemptKind
	WorkerID    string
	FenceEpoch  uint64
}

type ReconciledDeliveryClaim struct {
	Request     DeliveryOperationRequest
	Controller  DeliveryControllerIdentity
	RemoteState string
	// ReconcileOnly is permanent once an operator has resumed a quarantined
	// Operation. The worker may GET the exact ID/hash, but may never PUT it
	// again even when the controller reports 404.
	ReconcileOnly bool
	RunState      DeliveryRunState
	Preview       *PreviewClaim
	Production    *ProductionClaim
}

func (claim ReconciledDeliveryClaim) Lease() DeliveryLease {
	if claim.Preview != nil {
		return claim.Preview.Lease
	}
	if claim.Production != nil {
		return claim.Production.Lease
	}
	return DeliveryLease{}
}

type ReconciledDeliveryWorkerStore interface {
	ClaimDeliveryOperation(context.Context, string, time.Duration) (*ReconciledDeliveryClaim, error)
	RenewDeliveryOperation(context.Context, ReconciledDeliveryClaim, time.Duration) error
	BeginDeliveryAttempt(context.Context, ReconciledDeliveryClaim, DeliveryAttemptKind) (DeliveryOperationAttempt, error)
	RecordDeliveryObservation(context.Context, ReconciledDeliveryClaim, DeliveryOperationAttempt, DeliveryOperationObservation, time.Time) error
	RecordDeliveryUnknown(context.Context, ReconciledDeliveryClaim, DeliveryOperationAttempt, string, string, time.Time) error
	RecordDeliveryNotFound(context.Context, ReconciledDeliveryClaim, DeliveryOperationAttempt) error
	QuarantineDeliveryOperation(context.Context, ReconciledDeliveryClaim, DeliveryOperationAttempt, string, string) error
	FinalizeDeliveryOperation(context.Context, ReconciledDeliveryClaim) error
}

// ReconciledDeliveryWorker never infers a remote outcome from a transport
// outcome. A timed-out PUT is persisted as unknown and every later worker GETs
// the same operation ID/hash. A 404 may resubmit only that identical request.
type ReconciledDeliveryWorker struct {
	store          ReconciledDeliveryWorkerStore
	provider       DeliveryOperationProvider
	workerID       string
	leaseTTL       time.Duration
	reconcileDelay time.Duration
	now            func() time.Time
}

func NewReconciledDeliveryWorker(
	store ReconciledDeliveryWorkerStore,
	provider DeliveryOperationProvider,
	workerID string,
	leaseTTL, reconcileDelay time.Duration,
) (*ReconciledDeliveryWorker, error) {
	workerID = strings.TrimSpace(workerID)
	if store == nil || provider == nil || !boundedIdentifier(workerID, 128) ||
		leaseTTL < 5*time.Second || reconcileDelay < time.Second || reconcileDelay > time.Hour {
		return nil, errors.New("reconciled release worker requires a store, provider, stable worker, lease, and retry delay")
	}
	return &ReconciledDeliveryWorker{
		store: store, provider: provider, workerID: workerID,
		leaseTTL: leaseTTL, reconcileDelay: reconcileDelay, now: time.Now,
	}, nil
}

func (worker *ReconciledDeliveryWorker) RunOne(ctx context.Context) (bool, error) {
	claim, err := worker.store.ClaimDeliveryOperation(ctx, worker.workerID, worker.leaseTTL)
	if err != nil {
		if errors.Is(err, ErrNoDeliveryWork) {
			return false, nil
		}
		return false, err
	}
	if claim == nil {
		return false, nil
	}
	if claim.RunState == DeliveryVerifying {
		return true, worker.store.FinalizeDeliveryOperation(ctx, *claim)
	}
	if claim.RunState == DeliveryClaimed {
		return true, worker.execute(ctx, *claim, DeliveryAttemptSubmit)
	}
	if claim.RunState == DeliveryReconciling {
		return true, worker.execute(ctx, *claim, DeliveryAttemptReconcile)
	}
	return true, fmt.Errorf("%w: claimed release run has unsupported state %q", ErrBundleIntegrity, claim.RunState)
}

func (worker *ReconciledDeliveryWorker) execute(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	kind DeliveryAttemptKind,
) error {
	attempt, err := worker.store.BeginDeliveryAttempt(ctx, claim, kind)
	if err != nil {
		return err
	}
	observation, providerErr := worker.callProvider(ctx, claim, kind)
	if providerErr == nil {
		return worker.acceptObservation(ctx, claim, attempt, observation)
	}
	if kind == DeliveryAttemptReconcile && errors.Is(providerErr, ErrDeliveryOperationNotFound) {
		if claim.ReconcileOnly {
			return worker.quarantine(ctx, claim, attempt, "controller-history-still-unavailable",
				"the exact controller operation remains unavailable after governed reconciliation; resubmission is forbidden")
		}
		if claim.RemoteState != "prepared" && claim.RemoteState != "submit_unknown" {
			return worker.quarantine(ctx, claim, attempt, "controller-history-lost",
				"the controller returned not-found after previously acknowledging the exact operation")
		}
		if err := worker.store.RecordDeliveryNotFound(context.WithoutCancel(ctx), claim, attempt); err != nil {
			return errors.Join(providerErr, err)
		}
		resubmit, err := worker.store.BeginDeliveryAttempt(ctx, claim, DeliveryAttemptResubmit)
		if err != nil {
			return err
		}
		observation, providerErr = worker.callProvider(ctx, claim, DeliveryAttemptResubmit)
		if providerErr == nil {
			return worker.acceptObservation(ctx, claim, resubmit, observation)
		}
		attempt = resubmit
	}
	if errors.Is(providerErr, ErrDeliveryOperationConflict) ||
		errors.Is(providerErr, ErrDeliveryControllerProtocol) ||
		errors.Is(providerErr, ErrDeliveryControllerTrust) {
		return worker.quarantine(ctx, claim, attempt, "controller-authority-conflict", boundedDeliveryError(providerErr))
	}
	next := worker.now().UTC().Add(worker.reconcileDelay).Truncate(time.Microsecond)
	if err := worker.store.RecordDeliveryUnknown(
		context.WithoutCancel(ctx), claim, attempt, "controller-outcome-unknown", boundedDeliveryError(providerErr), next,
	); err != nil {
		return errors.Join(providerErr, err)
	}
	return nil
}

func (worker *ReconciledDeliveryWorker) callProvider(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	kind DeliveryAttemptKind,
) (DeliveryOperationObservation, error) {
	return runDeliveryProviderWithHeartbeat(
		ctx,
		worker.leaseTTL,
		func(renewCtx context.Context) error {
			return worker.store.RenewDeliveryOperation(renewCtx, claim, worker.leaseTTL)
		},
		func(providerCtx context.Context) (DeliveryOperationObservation, error) {
			if kind == DeliveryAttemptReconcile {
				return worker.provider.Reconcile(providerCtx, claim.Request)
			}
			return worker.provider.Submit(providerCtx, claim.Request)
		},
	)
}

func (worker *ReconciledDeliveryWorker) acceptObservation(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	observation DeliveryOperationObservation,
) error {
	parsed, err := ParseDeliveryOperationObservation(observation, claim.Controller, claim.Request)
	if err != nil {
		return worker.quarantine(ctx, claim, attempt, "invalid-controller-observation", boundedDeliveryError(err))
	}
	next := time.Time{}
	if parsed.State == DeliveryRemoteAccepted || parsed.State == DeliveryRemoteRunning {
		next = worker.now().UTC().Add(worker.reconcileDelay).Truncate(time.Microsecond)
	}
	if err := worker.store.RecordDeliveryObservation(ctx, claim, attempt, parsed, next); err != nil {
		return err
	}
	if parsed.State == DeliveryRemoteCompleted {
		return worker.store.FinalizeDeliveryOperation(ctx, claim)
	}
	return nil
}

func (worker *ReconciledDeliveryWorker) quarantine(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	code, detail string,
) error {
	if err := worker.store.QuarantineDeliveryOperation(
		context.WithoutCancel(ctx), claim, attempt, code, detail,
	); err != nil {
		return err
	}
	return nil
}

func boundedDeliveryError(err error) string {
	if err == nil {
		return "release delivery controller failed without an error"
	}
	value := strings.TrimSpace(err.Error())
	if value == "" {
		value = "release delivery controller failed"
	}
	if len(value) > 2000 {
		value = value[:2000]
	}
	return strings.ReplaceAll(value, "\x00", "")
}

type ReconciledDeliveryWorkerService struct {
	worker       *ReconciledDeliveryWorker
	pollInterval time.Duration
	onError      func(error)
}

func (service *ReconciledDeliveryWorkerService) SetErrorHandler(handler func(error)) {
	if service != nil {
		service.onError = handler
	}
}

func NewReconciledDeliveryWorkerService(
	worker *ReconciledDeliveryWorker,
	pollInterval time.Duration,
) (*ReconciledDeliveryWorkerService, error) {
	if worker == nil || pollInterval <= 0 {
		return nil, errors.New("reconciled release worker and positive poll interval are required")
	}
	return &ReconciledDeliveryWorkerService{worker: worker, pollInterval: pollInterval}, nil
}

func (service *ReconciledDeliveryWorkerService) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		worked, err := service.worker.RunOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			// One quarantined or temporarily unavailable operation must not stop
			// reconciliation for unrelated projects. The exact error is persisted
			// by the store where an outcome was ambiguous.
			if service.onError != nil {
				service.onError(err)
			}
			timer.Reset(service.pollInterval)
			continue
		}
		if worked {
			timer.Reset(0)
		} else {
			timer.Reset(service.pollInterval)
		}
	}
}
