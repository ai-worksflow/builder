package release

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

func deploymentBundleFixture(t *testing.T) Bundle {
	t.Helper()
	receipt := passingCanonicalReceipt(t)
	bundle, err := NewBundle(NewBundleInput{
		ID: uuid.NewString(), Receipt: receipt, CreatedBy: receipt.CreatedBy, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestPreviewApprovalPromotionAndRollbackPreserveExactBundle(t *testing.T) {
	bundle := deploymentBundleFixture(t)
	actorID := uuid.NewString()
	preview, err := NewPreviewReceipt(NewPreviewReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), Bundle: bundle,
		Namespace: "preview-project-release", Provider: "kubernetes", ProviderRef: "namespace/preview-project-release",
		Checks:   passingReleasePreviewChecks(),
		Decision: PreviewPassed, CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := ParsePreviewReceipt(preview); err != nil || parsed.PayloadHash != preview.PayloadHash {
		t.Fatalf("PreviewReceipt did not round trip: %+v %v", parsed, err)
	}
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: preview, Reason: "Promote the exact healthy preview.",
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	promoteRunID := uuid.NewString()
	promoteChecks := passingReleaseProductionChecks()
	promoteReceipt, err := NewProductionReceipt(NewProductionReceiptInput{
		ID: uuid.NewString(), RunID: promoteRunID, Bundle: bundle, Preview: preview, Approval: approval,
		Operation: DeploymentPromote, Provider: "kubernetes", ProviderRef: "deployment/application@42",
		PublicURL: "https://application.example.test", Checks: promoteChecks, Decision: PreviewPassed,
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := NewDeploymentRevision(NewDeploymentRevisionInput{
		ID: uuid.NewString(), RunID: promoteRunID, Bundle: bundle, Preview: preview, Approval: approval,
		Receipt:   promoteReceipt,
		Operation: DeploymentPromote, Provider: "kubernetes", ProviderRef: "deployment/application@42",
		PublicURL: "https://application.example.test",
		Checks:    promoteChecks,
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	promotedRef, err := promoted.ExactReference()
	if err != nil {
		t.Fatal(err)
	}
	rollbackRunID := uuid.NewString()
	rollbackChecks := passingReleaseProductionChecks()
	rollbackReceipt, err := NewProductionReceipt(NewProductionReceiptInput{
		ID: uuid.NewString(), RunID: rollbackRunID, Bundle: bundle, Preview: preview, Approval: approval,
		Operation: DeploymentRollback, SourceRevision: &promotedRef,
		Provider: "kubernetes", ProviderRef: "deployment/application@43",
		PublicURL: "https://application.example.test", Checks: rollbackChecks, Decision: PreviewPassed,
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := NewDeploymentRevision(NewDeploymentRevisionInput{
		ID: uuid.NewString(), RunID: rollbackRunID, Bundle: bundle, Preview: preview, Approval: approval,
		Receipt:   rollbackReceipt,
		Operation: DeploymentRollback, SourceRevision: &promotedRef,
		Provider: "kubernetes", ProviderRef: "deployment/application@43",
		PublicURL: "https://application.example.test",
		Checks:    rollbackChecks,
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.ReleaseBundle != promoted.ReleaseBundle || rolledBack.SourceRevision == nil ||
		*rolledBack.SourceRevision != promotedRef || rolledBack.ID == promoted.ID {
		t.Fatalf("rollback did not create a new immutable revision over the exact old Bundle: %+v", rolledBack)
	}
}

func TestPromotionFailsClosedOnPreviewOrBundleDrift(t *testing.T) {
	bundle := deploymentBundleFixture(t)
	actorID := uuid.NewString()
	preview, err := NewPreviewReceipt(NewPreviewReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), Bundle: bundle,
		Namespace: "preview", Provider: "fake", ProviderRef: "preview/ref",
		Checks:   passingReleasePreviewChecks(),
		Decision: PreviewPassed, CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: preview, Reason: "approved", CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	drifted := bundle
	drifted.ID = uuid.NewString()
	drifted.BundleHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runID := uuid.NewString()
	receipt, err := NewProductionReceipt(NewProductionReceiptInput{
		ID: uuid.NewString(), RunID: runID, Bundle: bundle, Preview: preview, Approval: approval,
		Operation: DeploymentPromote, Provider: "fake", ProviderRef: "production/ref",
		PublicURL: "https://example.test", Checks: passingReleaseProductionChecks(),
		Decision: PreviewPassed, CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDeploymentRevision(NewDeploymentRevisionInput{
		ID: uuid.NewString(), RunID: runID, Bundle: drifted, Preview: preview, Approval: approval, Receipt: receipt,
		Operation: DeploymentPromote, Provider: "fake", ProviderRef: "production/ref", PublicURL: "https://example.test",
		Checks:    passingReleaseProductionChecks(),
		CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("promotion accepted a drifted ReleaseBundle")
	}
	tamperedApproval := approval
	tamperedApproval.ReleaseBundle = repository.ExactReference{ID: uuid.NewString(), ContentHash: approval.ReleaseBundle.ContentHash}
	if _, err := ParsePromotionApproval(tamperedApproval); err == nil {
		t.Fatal("tampered PromotionApproval was accepted")
	}
}

func TestFailedProductionReceiptIsImmutableEvidenceButCannotCreateRevision(t *testing.T) {
	bundle := deploymentBundleFixture(t)
	actorID := uuid.NewString()
	preview, err := NewPreviewReceipt(NewPreviewReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), Bundle: bundle,
		Namespace: "preview", Provider: "fake", ProviderRef: "preview/ref",
		Checks:   passingReleasePreviewChecks(),
		Decision: PreviewPassed, CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: preview, Reason: "approved", CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.NewString()
	checks := []PreviewCheck{{ID: "health", Kind: "health", Status: "failed", Detail: "503"}}
	receipt, err := NewProductionReceipt(NewProductionReceiptInput{
		ID: uuid.NewString(), RunID: runID, Bundle: bundle, Preview: preview, Approval: approval,
		Operation: DeploymentPromote, Provider: "fake", ProviderRef: "production/ref", Checks: checks,
		Decision: PreviewFailed, CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProductionReceipt(receipt); err != nil {
		t.Fatalf("failed ProductionReceipt did not round trip: %v", err)
	}
	if _, err := receipt.PassedReference(); err == nil {
		t.Fatal("failed ProductionReceipt produced a passing exact reference")
	}
	if _, err := NewDeploymentRevision(NewDeploymentRevisionInput{
		ID: uuid.NewString(), RunID: runID, Bundle: bundle, Preview: preview, Approval: approval, Receipt: receipt,
		Operation: DeploymentPromote, Provider: "fake", ProviderRef: "production/ref",
		PublicURL: "https://example.test", Checks: checks, CreatedBy: actorID, CreatedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("failed ProductionReceipt created a DeploymentRevision")
	}
}

func passingReleasePreviewChecks() []PreviewCheck {
	return []PreviewCheck{
		{ID: "contract", Kind: "contract", Status: "passed"},
		{ID: "health", Kind: "health", Status: "passed"},
		{ID: "migration", Kind: "migration", Status: "passed"},
		{ID: "playwright", Kind: "e2e", Status: "passed"},
		{ID: "smoke", Kind: "smoke", Status: "passed"},
	}
}

func passingReleaseProductionChecks() []PreviewCheck {
	return []PreviewCheck{
		{ID: "readiness", Kind: "health", Status: "passed"},
		{ID: "rollout-health", Kind: "rollout", Status: "passed"},
	}
}
