package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

var (
	ErrNoDeliveryWork = errors.New("no release delivery work is available")
	ErrDeliveryFence  = errors.New("release delivery worker fence was lost")
)

type DeliveryLease struct {
	WorkerID  string
	Epoch     uint64
	ExpiresAt time.Time
}

type PreviewClaim struct {
	Run    PreviewRun
	Bundle Bundle
	Lease  DeliveryLease
}

type ProductionClaim struct {
	Run      ProductionRun
	Bundle   Bundle
	Preview  PreviewReceipt
	Approval PromotionApproval
	Source   *DeploymentRevision
	Lease    DeliveryLease
}

type PreviewProviderRequest struct {
	RunID     string
	ProjectID string
	Namespace string
	Bundle    Bundle
}

type PreviewProviderResult struct {
	Provider    string         `json:"provider"`
	ProviderRef string         `json:"providerRef"`
	Checks      []PreviewCheck `json:"checks"`
}

type ProductionProviderRequest struct {
	RunID           string
	ProjectID       string
	Environment     string
	Bundle          Bundle
	Preview         PreviewReceipt
	Approval        PromotionApproval
	Operation       DeploymentOperation
	Source          *DeploymentRevision
	ExpectedHead    *repository.ExactReference
	ExpectedReceipt *repository.ExactReference
}

type ProductionProviderResult struct {
	Provider    string         `json:"provider"`
	ProviderRef string         `json:"providerRef"`
	PublicURL   string         `json:"publicUrl"`
	Checks      []PreviewCheck `json:"checks"`
}

type DeliveryProvider interface {
	// Implementations must be idempotent by RunID because an expired fenced
	// attempt can be reclaimed after an infrastructure crash.
	Preview(context.Context, PreviewProviderRequest) (PreviewProviderResult, error)
	DeployProduction(context.Context, ProductionProviderRequest) (ProductionProviderResult, error)
}

type DeliveryWorkerStore interface {
	ClaimPreview(context.Context, string, time.Duration) (*PreviewClaim, error)
	RenewPreview(context.Context, PreviewClaim, time.Duration) error
	AdvancePreview(context.Context, PreviewClaim, DeliveryRunState, DeliveryRunState) (PreviewClaim, error)
	CompletePreview(context.Context, PreviewClaim, PreviewProviderResult) (PreviewRun, error)
	FailPreview(context.Context, PreviewClaim, DeliveryRunState) error
	ClaimProduction(context.Context, string, time.Duration) (*ProductionClaim, error)
	RenewProduction(context.Context, ProductionClaim, time.Duration) error
	AdvanceProduction(context.Context, ProductionClaim, DeliveryRunState, DeliveryRunState) (ProductionClaim, error)
	CompleteProduction(context.Context, ProductionClaim, ProductionProviderResult) (ProductionRun, error)
	FailProduction(context.Context, ProductionClaim, DeliveryRunState) error
}

type DeliveryWorker struct {
	store    DeliveryWorkerStore
	provider DeliveryProvider
	workerID string
	leaseTTL time.Duration
}

func NewDeliveryWorker(
	store DeliveryWorkerStore,
	provider DeliveryProvider,
	workerID string,
	leaseTTL time.Duration,
) (*DeliveryWorker, error) {
	workerID = strings.TrimSpace(workerID)
	if store == nil || provider == nil || !boundedIdentifier(workerID, 128) || leaseTTL < 5*time.Second {
		return nil, errors.New("release delivery worker store, provider, stable worker identity, and lease TTL are required")
	}
	return &DeliveryWorker{store: store, provider: provider, workerID: workerID, leaseTTL: leaseTTL}, nil
}

func (worker *DeliveryWorker) RunOne(ctx context.Context) (bool, error) {
	preview, err := worker.store.ClaimPreview(ctx, worker.workerID, worker.leaseTTL)
	if err != nil && !errors.Is(err, ErrNoDeliveryWork) {
		return false, err
	}
	if preview != nil {
		return true, worker.runPreview(ctx, *preview)
	}
	production, err := worker.store.ClaimProduction(ctx, worker.workerID, worker.leaseTTL)
	if err != nil && !errors.Is(err, ErrNoDeliveryWork) {
		return false, err
	}
	if production == nil {
		return false, nil
	}
	return true, worker.runProduction(ctx, *production)
}

func (worker *DeliveryWorker) runPreview(ctx context.Context, claim PreviewClaim) error {
	advanced, err := worker.store.AdvancePreview(ctx, claim, DeliveryClaimed, DeliveryDeploying)
	if err != nil {
		return err
	}
	claim = advanced
	result, err := runDeliveryProviderWithHeartbeat(
		ctx, worker.leaseTTL,
		func(renewCtx context.Context) error {
			return worker.store.RenewPreview(renewCtx, claim, worker.leaseTTL)
		},
		func(providerCtx context.Context) (PreviewProviderResult, error) {
			return worker.provider.Preview(providerCtx, PreviewProviderRequest{
				RunID: claim.Run.ID, ProjectID: claim.Run.ProjectID,
				Namespace: deterministicPreviewNamespace(claim.Run.ProjectID, claim.Bundle.ID), Bundle: claim.Bundle,
			})
		},
	)
	if err != nil {
		if failErr := worker.store.FailPreview(context.WithoutCancel(ctx), claim, DeliveryError); failErr != nil {
			return errors.Join(fmt.Errorf("release preview provider: %w", err), failErr)
		}
		return nil
	}
	advanced, err = worker.store.AdvancePreview(ctx, claim, DeliveryDeploying, DeliveryVerifying)
	if err != nil {
		return err
	}
	claim = advanced
	_, err = worker.store.CompletePreview(ctx, claim, result)
	return err
}

func (worker *DeliveryWorker) runProduction(ctx context.Context, claim ProductionClaim) error {
	advanced, err := worker.store.AdvanceProduction(ctx, claim, DeliveryClaimed, DeliveryDeploying)
	if err != nil {
		return err
	}
	claim = advanced
	result, err := runDeliveryProviderWithHeartbeat(
		ctx, worker.leaseTTL,
		func(renewCtx context.Context) error {
			return worker.store.RenewProduction(renewCtx, claim, worker.leaseTTL)
		},
		func(providerCtx context.Context) (ProductionProviderResult, error) {
			return worker.provider.DeployProduction(providerCtx, ProductionProviderRequest{
				RunID: claim.Run.ID, ProjectID: claim.Run.ProjectID, Environment: claim.Run.Environment, Bundle: claim.Bundle,
				Preview: claim.Preview, Approval: claim.Approval, Operation: claim.Run.Operation, Source: claim.Source,
				ExpectedHead: claim.Run.ExpectedRevision, ExpectedReceipt: claim.Run.ExpectedReceipt,
			})
		},
	)
	if err != nil {
		if failErr := worker.store.FailProduction(context.WithoutCancel(ctx), claim, DeliveryError); failErr != nil {
			return errors.Join(fmt.Errorf("release production provider: %w", err), failErr)
		}
		return nil
	}
	advanced, err = worker.store.AdvanceProduction(ctx, claim, DeliveryDeploying, DeliveryVerifying)
	if err != nil {
		return err
	}
	claim = advanced
	_, err = worker.store.CompleteProduction(ctx, claim, result)
	return err
}

func deterministicPreviewNamespace(projectID, bundleID string) string {
	value := uuid.NewSHA1(uuid.NameSpaceOID, []byte("release-preview\x00"+projectID+"\x00"+bundleID)).String()
	return "preview-" + strings.ReplaceAll(value, "-", "")[:24]
}

type deliveryProviderOutcome[T any] struct {
	value T
	err   error
}

func runDeliveryProviderWithHeartbeat[T any](
	ctx context.Context,
	leaseTTL time.Duration,
	renew func(context.Context) error,
	work func(context.Context) (T, error),
) (T, error) {
	providerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcome := make(chan deliveryProviderOutcome[T], 1)
	go func() {
		value, err := work(providerCtx)
		outcome <- deliveryProviderOutcome[T]{value: value, err: err}
	}()
	interval := leaseTTL / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case result := <-outcome:
			return result.value, result.err
		case <-ticker.C:
			renewCtx, renewCancel := context.WithTimeout(ctx, interval)
			err := renew(renewCtx)
			renewCancel()
			if err != nil {
				cancel()
				var zero T
				return zero, fmt.Errorf("renew release delivery lease: %w", err)
			}
		}
	}
}

type DeliveryWorkerService struct {
	worker       *DeliveryWorker
	pollInterval time.Duration
}

func NewDeliveryWorkerService(worker *DeliveryWorker, pollInterval time.Duration) (*DeliveryWorkerService, error) {
	if worker == nil || pollInterval <= 0 {
		return nil, errors.New("release delivery worker and positive poll interval are required")
	}
	return &DeliveryWorkerService{worker: worker, pollInterval: pollInterval}, nil
}

func (service *DeliveryWorkerService) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			worked, err := service.worker.RunOne(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			if worked {
				timer.Reset(time.Millisecond)
			} else {
				timer.Reset(service.pollInterval)
			}
		}
	}
}
