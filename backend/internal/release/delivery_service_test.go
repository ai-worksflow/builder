package release

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/repository"
)

type deliveryAccessFake struct {
	action core.Action
	err    error
}

func TestProductionHeadConflictClassificationIsNarrow(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "active environment",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "release_deployment_runs_one_nonterminal_environment_idx"},
			want: true,
		},
		{
			name: "stale expected head",
			err:  &pgconn.PgError{Code: "40001", Message: "release deployment Run expected production head is stale"},
			want: true,
		},
		{
			name: "unrelated uniqueness",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "release_deployment_run_request_unique"},
			want: false,
		},
		{
			name: "unrelated serialization failure",
			err:  &pgconn.PgError{Code: "40001", Message: "other workflow changed"},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isProductionHeadConflict(test.err); got != test.want {
				t.Fatalf("isProductionHeadConflict(%v)=%v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestPreviewRunConflictClassificationIsNarrow(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "active exact Bundle",
			err: &pgconn.PgError{
				Code: "23505", ConstraintName: "release_preview_runs_one_nonterminal_bundle_idx",
			},
			want: true,
		},
		{
			name: "request idempotency",
			err: &pgconn.PgError{
				Code: "23505", ConstraintName: "release_preview_run_request_unique",
			},
			want: false,
		},
		{
			name: "generic serialization",
			err:  &pgconn.PgError{Code: "40001", Message: "could not serialize access"},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPreviewRunConflict(test.err); got != test.want {
				t.Fatalf("isPreviewRunConflict(%v)=%v, want %v", test.err, got, test.want)
			}
		})
	}
}

func (fake *deliveryAccessFake) Authorize(_ context.Context, _, _ string, action core.Action) (core.Role, error) {
	fake.action = action
	return core.RoleOwner, fake.err
}

type deliveryControlStoreFake struct {
	bundle              Bundle
	preview             PreviewReceipt
	approval            PromotionApproval
	receipt             ProductionReceipt
	revision            DeploymentRevision
	previewRun          PreviewRun
	productionRun       ProductionRun
	previewInput        CreatePreviewRunInput
	productionInput     CreateProductionRunInput
	reconciliationInput ResumeBlockedDeliveryInput
	reconciliationCase  DeliveryReconciliationCase
	reconciliationErr   error
}

func (fake *deliveryControlStoreFake) ResumeBlockedDelivery(
	_ context.Context,
	input ResumeBlockedDeliveryInput,
) (DeliveryReconciliationCase, bool, error) {
	fake.reconciliationInput = input
	return fake.reconciliationCase, false, fake.reconciliationErr
}

func (fake *deliveryControlStoreFake) GetDeliveryReconciliationCase(
	context.Context, string, string,
) (DeliveryReconciliationCase, error) {
	return fake.reconciliationCase, fake.reconciliationErr
}

func (fake *deliveryControlStoreFake) ListDeliveryReconciliationCases(
	context.Context, string, int,
) ([]DeliveryReconciliationCase, error) {
	return []DeliveryReconciliationCase{fake.reconciliationCase}, fake.reconciliationErr
}

func (fake *deliveryControlStoreFake) GetBlockedDeliveryReconciliationSnapshot(
	context.Context, string, DeliveryOperationKind, string,
) (DeliveryReconciliationBlockSnapshot, error) {
	return DeliveryReconciliationBlockSnapshot{}, fake.reconciliationErr
}

func TestDeliveryServiceRequiresAdminForBlockedReconciliation(t *testing.T) {
	projectID, runID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	access := &deliveryAccessFake{}
	store := &deliveryControlStoreFake{}
	service, err := NewDeliveryService(store, access)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.ResumeBlockedDelivery(context.Background(), ResumeBlockedDeliveryRequest{
		ProjectID: projectID, RunKind: DeliveryOperationPreview, RunID: runID,
		ExpectedVersion: 4, ExpectedErrorCode: "controller-authority-conflict",
		Reason: "controller history was repaired", ActorID: actorID, OperationID: "reconcile-once",
	})
	if err != nil {
		t.Fatal(err)
	}
	if access.action != core.ActionAdmin {
		t.Fatalf("authorization action=%q, want admin", access.action)
	}
	if store.reconciliationInput.ProjectID != projectID || store.reconciliationInput.RunID != runID ||
		store.reconciliationInput.ExpectedVersion != 4 ||
		store.reconciliationInput.ExpectedErrorCode != "controller-authority-conflict" ||
		store.reconciliationInput.IdempotencyKey != "reconcile-once" ||
		!exactHash(store.reconciliationInput.RequestHash) {
		t.Fatalf("reconciliation input=%+v", store.reconciliationInput)
	}
}

func TestDeliveryServiceBlockedSnapshotIsAdminOnlyButCaseAuditUsesView(t *testing.T) {
	projectID, runID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	access := &deliveryAccessFake{err: core.ErrForbidden}
	service, err := NewDeliveryService(&deliveryControlStoreFake{}, access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetBlockedDeliveryReconciliationSnapshot(
		context.Background(), projectID, DeliveryOperationPreview, runID, actorID,
	); !errors.Is(err, core.ErrForbidden) || access.action != core.ActionAdmin {
		t.Fatalf("snapshot err=%v action=%q, want admin denial", err, access.action)
	}
	access.err = nil
	if _, err := service.ListDeliveryReconciliationCases(context.Background(), projectID, actorID); err != nil {
		t.Fatal(err)
	}
	if access.action != core.ActionView {
		t.Fatalf("immutable Case audit action=%q, want view", access.action)
	}
}

func (fake *deliveryControlStoreFake) Get(context.Context, string, string, string) (Bundle, error) {
	return fake.bundle, nil
}

func (fake *deliveryControlStoreFake) CreatePreviewRun(_ context.Context, input CreatePreviewRunInput) (PreviewRun, bool, error) {
	fake.previewInput = input
	fake.previewRun = PreviewRun{
		ID: input.ID, ProjectID: input.ProjectID, ReleaseBundle: input.ReleaseBundle,
		Reason: input.Reason, State: DeliveryQueued, Version: 1, CreatedBy: input.CreatedBy,
	}
	return fake.previewRun, false, nil
}

func (fake *deliveryControlStoreFake) GetPreviewRun(context.Context, string, string) (PreviewRun, error) {
	return fake.previewRun, nil
}

func (fake *deliveryControlStoreFake) ListPreviewRuns(context.Context, string, repository.ExactReference, int) ([]PreviewRun, error) {
	return []PreviewRun{fake.previewRun}, nil
}

func (fake *deliveryControlStoreFake) GetPreviewReceipt(context.Context, string, string, string) (PreviewReceipt, error) {
	return fake.preview, nil
}

func (fake *deliveryControlStoreFake) SavePromotionApproval(_ context.Context, approval PromotionApproval) (PromotionApproval, bool, error) {
	fake.approval = approval
	return approval, false, nil
}

func (fake *deliveryControlStoreFake) GetPromotionApproval(context.Context, string, string, string) (PromotionApproval, error) {
	return fake.approval, nil
}

func (fake *deliveryControlStoreFake) GetPromotionApprovalByPreview(context.Context, string, string, string) (PromotionApproval, error) {
	return fake.approval, nil
}

func (fake *deliveryControlStoreFake) CreateProductionRun(_ context.Context, input CreateProductionRunInput) (ProductionRun, bool, error) {
	fake.productionInput = input
	fake.productionRun = ProductionRun{
		ID: input.ID, ProjectID: input.ProjectID, Environment: input.Environment, Operation: input.Operation,
		ReleaseBundle: input.ReleaseBundle, PreviewReceipt: input.PreviewReceipt,
		PromotionApproval: input.PromotionApproval, SourceRevision: cloneExactReference(input.SourceRevision),
		Reason: input.Reason, State: DeliveryQueued, Version: 1, CreatedBy: input.CreatedBy,
	}
	return fake.productionRun, false, nil
}

func (fake *deliveryControlStoreFake) GetProductionRun(context.Context, string, string) (ProductionRun, error) {
	return fake.productionRun, nil
}

func (fake *deliveryControlStoreFake) ListProductionRuns(context.Context, string, repository.ExactReference, int) ([]ProductionRun, error) {
	return []ProductionRun{fake.productionRun}, nil
}

func (fake *deliveryControlStoreFake) ListProductionRunsForProject(context.Context, string, int) ([]ProductionRun, error) {
	return []ProductionRun{fake.productionRun}, nil
}

func (fake *deliveryControlStoreFake) GetProductionReceipt(context.Context, string, string, string) (ProductionReceipt, error) {
	return fake.receipt, nil
}

func (fake *deliveryControlStoreFake) GetDeploymentRevision(context.Context, string, string, string) (DeploymentRevision, error) {
	return fake.revision, nil
}

func deliveryServiceFixture(t *testing.T) (*DeliveryService, *deliveryControlStoreFake, *deliveryAccessFake) {
	t.Helper()
	bundle := deploymentBundleFixture(t)
	actorID := bundle.CreatedBy
	previewController := ControllerOperationResultReference{
		OperationID: uuid.NewString(), ResultHash: "sha256:" + strings.Repeat("a", 64),
	}
	preview, err := NewPreviewReceiptV2(NewPreviewReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), Bundle: bundle,
		Namespace: "preview", Provider: "fake", ProviderRef: "preview/ref",
		Checks:   passingReleasePreviewChecks(),
		Decision: PreviewPassed, CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	}, previewController)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: preview, Reason: "Approve exact preview.",
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.NewString()
	productionController := ControllerOperationResultReference{
		OperationID: runID, ResultHash: "sha256:" + strings.Repeat("b", 64),
	}
	checks := passingReleaseProductionChecks()
	receipt, err := NewProductionReceiptV2(NewProductionReceiptInput{
		ID: uuid.NewString(), RunID: runID, Bundle: bundle, Preview: preview, Approval: approval,
		Operation: DeploymentPromote, Provider: "fake", ProviderRef: "production/ref",
		PublicURL: "https://production.example.test", Checks: checks, Decision: PreviewPassed,
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	}, productionController)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := NewDeploymentRevisionV2(NewDeploymentRevisionInput{
		ID: uuid.NewString(), RunID: runID, Bundle: bundle, Preview: preview, Approval: approval, Receipt: receipt,
		Operation: DeploymentPromote, Provider: "fake", ProviderRef: "production/ref",
		PublicURL: "https://production.example.test",
		Checks:    checks,
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	}, productionController)
	if err != nil {
		t.Fatal(err)
	}
	store := &deliveryControlStoreFake{
		bundle: bundle, preview: preview, approval: approval, receipt: receipt, revision: revision,
	}
	access := &deliveryAccessFake{}
	service, err := NewDeliveryService(store, access)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
	return service, store, access
}

func TestDeliveryServiceClosesPreviewApprovalPromotionRollbackLineage(t *testing.T) {
	service, store, access := deliveryServiceFixture(t)
	actorID, projectID := store.bundle.CreatedBy, store.bundle.ProjectID
	previewRun, replayed, err := service.StartPreview(context.Background(), StartPreviewRequest{
		ProjectID: projectID, ReleaseBundleID: store.bundle.ID, ReleaseBundleHash: store.bundle.BundleHash,
		Reason: "Deploy exact Bundle to isolated preview.", ActorID: actorID, OperationID: "preview-1",
	})
	if err != nil || replayed || access.action != core.ActionEdit || previewRun.ReleaseBundle.ID != store.bundle.ID {
		t.Fatalf("start preview = %+v replayed=%v action=%s err=%v", previewRun, replayed, access.action, err)
	}
	approval, replayed, err := service.ApprovePromotion(context.Background(), ApprovePromotionRequest{
		ProjectID: projectID, PreviewReceiptID: store.preview.ID, PreviewReceiptHash: store.preview.PayloadHash,
		Reason: "Approve the exact passed preview.", ActorID: actorID, OperationID: "approval-1",
	})
	if err != nil || replayed || access.action != core.ActionPublish || approval.ReleaseBundle.ID != store.bundle.ID {
		t.Fatalf("approve promotion = %+v replayed=%v action=%s err=%v", approval, replayed, access.action, err)
	}
	promoted, replayed, err := service.StartPromotion(context.Background(), StartPromotionRequest{
		ProjectID: projectID, PromotionApprovalID: approval.ID, PromotionApprovalHash: approval.PayloadHash,
		Reason: "Promote without rebuilding.", ActorID: actorID, OperationID: "promote-1",
	})
	if err != nil || replayed || promoted.Operation != DeploymentPromote ||
		promoted.Environment != "production" || store.productionInput.Environment != "production" ||
		promoted.ReleaseBundle != store.bundleReference() || promoted.SourceRevision != nil {
		t.Fatalf("start promotion = %+v replayed=%v err=%v", promoted, replayed, err)
	}
	sourceRef, _ := store.revision.ExactReference()
	rolledBack, replayed, err := service.StartRollback(context.Background(), StartRollbackRequest{
		ProjectID: projectID, SourceRevisionID: sourceRef.ID, SourceRevisionHash: sourceRef.ContentHash,
		Reason: "Rollback by reusing the previous immutable Bundle.", ActorID: actorID, OperationID: "rollback-1",
	})
	if err != nil || replayed || rolledBack.Operation != DeploymentRollback ||
		rolledBack.SourceRevision == nil || *rolledBack.SourceRevision != sourceRef ||
		rolledBack.ReleaseBundle != store.revision.ReleaseBundle {
		t.Fatalf("start rollback = %+v replayed=%v err=%v", rolledBack, replayed, err)
	}
}

func TestDeliveryServiceFailsBeforeStoreOnInvalidOrUnauthorizedMutation(t *testing.T) {
	service, store, access := deliveryServiceFixture(t)
	_, _, err := service.StartPreview(context.Background(), StartPreviewRequest{
		ProjectID: store.bundle.ProjectID, ReleaseBundleID: store.bundle.ID,
		ReleaseBundleHash: "", Reason: "preview", ActorID: store.bundle.CreatedBy, OperationID: "preview",
	})
	if !errors.Is(err, ErrInvalidBundle) || access.action != "" || store.previewInput.ID != "" {
		t.Fatalf("invalid preview mutation crossed boundary: action=%s input=%+v err=%v", access.action, store.previewInput, err)
	}
	access.err = core.ErrForbidden
	_, _, err = service.StartPreview(context.Background(), StartPreviewRequest{
		ProjectID: store.bundle.ProjectID, ReleaseBundleID: store.bundle.ID,
		ReleaseBundleHash: store.bundle.BundleHash, Reason: "preview", ActorID: store.bundle.CreatedBy,
		OperationID: "preview-denied",
	})
	if !errors.Is(err, core.ErrForbidden) || store.previewInput.ID != "" {
		t.Fatalf("unauthorized preview mutation crossed boundary: input=%+v err=%v", store.previewInput, err)
	}
}

func (fake *deliveryControlStoreFake) bundleReference() repository.ExactReference {
	return repository.ExactReference{ID: fake.bundle.ID, ContentHash: fake.bundle.BundleHash}
}
