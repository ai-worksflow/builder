package release

import (
	"context"
	"errors"
	"testing"
	"time"
)

type deliveryWorkerStoreFake struct {
	preview          *PreviewClaim
	production       *ProductionClaim
	previewStates    []DeliveryRunState
	productionStates []DeliveryRunState
	previewResult    PreviewProviderResult
	productionResult ProductionProviderResult
	previewFailed    DeliveryRunState
	productionFailed DeliveryRunState
}

func (fake *deliveryWorkerStoreFake) ClaimPreview(context.Context, string, time.Duration) (*PreviewClaim, error) {
	claim := fake.preview
	fake.preview = nil
	if claim == nil {
		return nil, ErrNoDeliveryWork
	}
	return claim, nil
}

func (fake *deliveryWorkerStoreFake) AdvancePreview(_ context.Context, claim PreviewClaim, from, to DeliveryRunState) (PreviewClaim, error) {
	if claim.Run.State != from {
		return PreviewClaim{}, ErrDeliveryFence
	}
	claim.Run.State = to
	fake.previewStates = append(fake.previewStates, to)
	return claim, nil
}

func (fake *deliveryWorkerStoreFake) RenewPreview(context.Context, PreviewClaim, time.Duration) error {
	return nil
}

func (fake *deliveryWorkerStoreFake) CompletePreview(_ context.Context, claim PreviewClaim, result PreviewProviderResult) (PreviewRun, error) {
	fake.previewResult = result
	claim.Run.State = DeliveryPassed
	return claim.Run, nil
}

func (fake *deliveryWorkerStoreFake) FailPreview(_ context.Context, _ PreviewClaim, state DeliveryRunState) error {
	fake.previewFailed = state
	return nil
}

func (fake *deliveryWorkerStoreFake) ClaimProduction(context.Context, string, time.Duration) (*ProductionClaim, error) {
	claim := fake.production
	fake.production = nil
	if claim == nil {
		return nil, ErrNoDeliveryWork
	}
	return claim, nil
}

func (fake *deliveryWorkerStoreFake) AdvanceProduction(_ context.Context, claim ProductionClaim, from, to DeliveryRunState) (ProductionClaim, error) {
	if claim.Run.State != from {
		return ProductionClaim{}, ErrDeliveryFence
	}
	claim.Run.State = to
	fake.productionStates = append(fake.productionStates, to)
	return claim, nil
}

func (fake *deliveryWorkerStoreFake) RenewProduction(context.Context, ProductionClaim, time.Duration) error {
	return nil
}

func (fake *deliveryWorkerStoreFake) CompleteProduction(_ context.Context, claim ProductionClaim, result ProductionProviderResult) (ProductionRun, error) {
	fake.productionResult = result
	claim.Run.State = DeliveryHealthy
	return claim.Run, nil
}

func (fake *deliveryWorkerStoreFake) FailProduction(_ context.Context, _ ProductionClaim, state DeliveryRunState) error {
	fake.productionFailed = state
	return nil
}

type deliveryProviderFake struct {
	previewRequest    PreviewProviderRequest
	productionRequest ProductionProviderRequest
	previewResult     PreviewProviderResult
	productionResult  ProductionProviderResult
	err               error
}

func (fake *deliveryProviderFake) Preview(_ context.Context, request PreviewProviderRequest) (PreviewProviderResult, error) {
	fake.previewRequest = request
	return fake.previewResult, fake.err
}

func (fake *deliveryProviderFake) DeployProduction(_ context.Context, request ProductionProviderRequest) (ProductionProviderResult, error) {
	fake.productionRequest = request
	return fake.productionResult, fake.err
}

func TestDeliveryWorkerUsesExactBundleAcrossPreviewAndProduction(t *testing.T) {
	_, control, _ := deliveryServiceFixture(t)
	previewClaim := &PreviewClaim{
		Run: PreviewRun{
			ID: "11111111-1111-4111-8111-111111111111", ProjectID: control.bundle.ProjectID,
			ReleaseBundle: control.bundleReference(), State: DeliveryClaimed, CreatedBy: control.bundle.CreatedBy,
		},
		Bundle: control.bundle, Lease: DeliveryLease{WorkerID: "worker", Epoch: 1, ExpiresAt: time.Now().Add(time.Minute)},
	}
	store := &deliveryWorkerStoreFake{preview: previewClaim}
	provider := &deliveryProviderFake{previewResult: PreviewProviderResult{
		Provider: "fake", ProviderRef: "preview/ref",
		Checks: []PreviewCheck{{ID: "health", Kind: "health", Status: "passed"}},
	}}
	worker, err := NewDeliveryWorker(store, provider, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	worked, err := worker.RunOne(context.Background())
	if err != nil || !worked || provider.previewRequest.Bundle.BundleHash != control.bundle.BundleHash ||
		len(store.previewStates) != 2 || store.previewStates[0] != DeliveryDeploying ||
		store.previewStates[1] != DeliveryVerifying || store.previewResult.ProviderRef != "preview/ref" {
		t.Fatalf("preview worker did not preserve exact Bundle: provider=%+v store=%+v worked=%v err=%v", provider, store, worked, err)
	}

	expectedRevision := repositoryReference(control.revision.ID, control.revision.PayloadHash)
	expectedReceipt := repositoryReference(control.receipt.ID, control.receipt.PayloadHash)
	productionClaim := &ProductionClaim{
		Run: ProductionRun{
			ID: "22222222-2222-4222-8222-222222222222", ProjectID: control.bundle.ProjectID,
			Environment: "production",
			Operation:   DeploymentPromote, ReleaseBundle: control.bundleReference(),
			PreviewReceipt:    control.approval.PreviewReceipt,
			PromotionApproval: repositoryReference(control.approval.ID, control.approval.PayloadHash),
			ExpectedRevision:  &expectedRevision,
			ExpectedReceipt:   &expectedReceipt,
			State:             DeliveryClaimed, CreatedBy: control.bundle.CreatedBy,
		},
		Bundle: control.bundle, Preview: control.preview, Approval: control.approval,
		Lease: DeliveryLease{WorkerID: "worker", Epoch: 2, ExpiresAt: time.Now().Add(time.Minute)},
	}
	store.production = productionClaim
	provider.productionResult = ProductionProviderResult{
		Provider: "fake", ProviderRef: "production/ref", PublicURL: "https://production.example.test",
		Checks: []PreviewCheck{{ID: "health", Kind: "health", Status: "passed"}},
	}
	worked, err = worker.RunOne(context.Background())
	if err != nil || !worked || provider.productionRequest.Bundle.BundleHash != control.bundle.BundleHash ||
		provider.productionRequest.Environment != "production" ||
		provider.productionRequest.Preview.PayloadHash != control.preview.PayloadHash ||
		provider.productionRequest.Approval.PayloadHash != control.approval.PayloadHash ||
		provider.productionRequest.ExpectedHead == nil || *provider.productionRequest.ExpectedHead != expectedRevision ||
		provider.productionRequest.ExpectedReceipt == nil || *provider.productionRequest.ExpectedReceipt != expectedReceipt ||
		len(store.productionStates) != 2 || store.productionResult.ProviderRef != "production/ref" {
		t.Fatalf("production worker did not reuse exact preview authority: provider=%+v store=%+v worked=%v err=%v", provider, store, worked, err)
	}
}

func TestDeliveryWorkerProviderFailureEndsFencedRunAsError(t *testing.T) {
	_, control, _ := deliveryServiceFixture(t)
	store := &deliveryWorkerStoreFake{preview: &PreviewClaim{
		Run: PreviewRun{
			ID: "11111111-1111-4111-8111-111111111111", ProjectID: control.bundle.ProjectID,
			ReleaseBundle: control.bundleReference(), State: DeliveryClaimed, CreatedBy: control.bundle.CreatedBy,
		},
		Bundle: control.bundle, Lease: DeliveryLease{WorkerID: "worker", Epoch: 1, ExpiresAt: time.Now().Add(time.Minute)},
	}}
	provider := &deliveryProviderFake{err: errors.New("provider unavailable")}
	worker, err := NewDeliveryWorker(store, provider, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	worked, err := worker.RunOne(context.Background())
	if !worked || err != nil || store.previewFailed != DeliveryError {
		t.Fatalf("provider failure was not persisted as an error: worked=%v state=%s err=%v", worked, store.previewFailed, err)
	}
}
