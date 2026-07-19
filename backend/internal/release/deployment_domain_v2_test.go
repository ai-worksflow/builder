package release

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

type releaseEvidenceV2Fixture struct {
	bundle          Bundle
	previewInput    NewPreviewReceiptInput
	previewRef      ControllerOperationResultReference
	preview         PreviewReceipt
	approval        PromotionApproval
	productionInput NewProductionReceiptInput
	productionRef   ControllerOperationResultReference
	production      ProductionReceipt
	revisionInput   NewDeploymentRevisionInput
	revision        DeploymentRevision
}

type releaseEvidenceV1Fixture struct {
	bundle          Bundle
	previewInput    NewPreviewReceiptInput
	preview         PreviewReceipt
	approval        PromotionApproval
	productionInput NewProductionReceiptInput
	production      ProductionReceipt
	revisionInput   NewDeploymentRevisionInput
	revision        DeploymentRevision
}

func TestReleaseEvidenceV2RoundTripsWithExactControllerResult(t *testing.T) {
	fixture := newReleaseEvidenceV2Fixture(t)

	parsedPreview, err := ParsePreviewReceipt(fixture.preview)
	if err != nil || !reflect.DeepEqual(parsedPreview, fixture.preview) {
		t.Fatalf("PreviewReceipt v2 round trip: parsed=%+v err=%v", parsedPreview, err)
	}
	if parsedPreview.SchemaVersion != PreviewReceiptSchemaVersionV2 || parsedPreview.ControllerOperation == nil ||
		*parsedPreview.ControllerOperation != fixture.previewRef {
		t.Fatalf("PreviewReceipt v2 lost exact controller result: %+v", parsedPreview)
	}

	parsedProduction, err := ParseProductionReceipt(fixture.production)
	if err != nil || !reflect.DeepEqual(parsedProduction, fixture.production) {
		t.Fatalf("ProductionReceipt v2 round trip: parsed=%+v err=%v", parsedProduction, err)
	}
	if parsedProduction.SchemaVersion != ProductionReceiptSchemaVersionV2 || parsedProduction.ControllerOperation == nil ||
		*parsedProduction.ControllerOperation != fixture.productionRef {
		t.Fatalf("ProductionReceipt v2 lost exact controller result: %+v", parsedProduction)
	}

	parsedRevision, err := ParseDeploymentRevision(fixture.revision)
	if err != nil || !reflect.DeepEqual(parsedRevision, fixture.revision) {
		t.Fatalf("DeploymentRevision v2 round trip: parsed=%+v err=%v", parsedRevision, err)
	}
	if parsedRevision.SchemaVersion != DeploymentRevisionSchemaVersionV2 || parsedRevision.ControllerOperation == nil ||
		*parsedRevision.ControllerOperation != fixture.productionRef {
		t.Fatalf("DeploymentRevision v2 lost exact controller result: %+v", parsedRevision)
	}
}

func TestReleaseEvidenceV2PayloadHashesBindBothControllerResultFields(t *testing.T) {
	fixture := newReleaseEvidenceV2Fixture(t)
	tests := []struct {
		name      string
		reference ControllerOperationResultReference
		parse     func(*ControllerOperationResultReference) error
	}{
		{
			name:      "PreviewReceipt",
			reference: fixture.previewRef,
			parse: func(reference *ControllerOperationResultReference) error {
				value := fixture.preview
				value.ControllerOperation = reference
				_, err := ParsePreviewReceipt(value)
				return err
			},
		},
		{
			name:      "ProductionReceipt",
			reference: fixture.productionRef,
			parse: func(reference *ControllerOperationResultReference) error {
				value := fixture.production
				value.ControllerOperation = reference
				_, err := ParseProductionReceipt(value)
				return err
			},
		},
		{
			name:      "DeploymentRevision",
			reference: fixture.productionRef,
			parse: func(reference *ControllerOperationResultReference) error {
				value := fixture.revision
				value.ControllerOperation = reference
				_, err := ParseDeploymentRevision(value)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name+" operation id", func(t *testing.T) {
			tampered := test.reference
			tampered.OperationID = uuid.NewString()
			if err := test.parse(&tampered); err == nil {
				t.Fatal("validly shaped controller operation ID tamper was not detected by the payload hash")
			}
		})
		t.Run(test.name+" result hash", func(t *testing.T) {
			tampered := test.reference
			tampered.ResultHash = releaseEvidenceDifferentHash(tampered.ResultHash)
			if err := test.parse(&tampered); err == nil {
				t.Fatal("validly shaped controller result hash tamper was not detected by the payload hash")
			}
		})
	}

	alternatePreviewRef := fixture.previewRef
	alternatePreviewRef.ResultHash = releaseEvidenceDifferentHash(alternatePreviewRef.ResultHash)
	alternatePreview, err := NewPreviewReceiptV2(fixture.previewInput, alternatePreviewRef)
	if err != nil {
		t.Fatalf("create PreviewReceipt with alternate exact result: %v", err)
	}
	if alternatePreview.PayloadHash == fixture.preview.PayloadHash {
		t.Fatal("PreviewReceipt payload hash did not commit to the exact controller result")
	}

	alternateProductionRef := fixture.productionRef
	alternateProductionRef.ResultHash = releaseEvidenceDifferentHash(alternateProductionRef.ResultHash)
	alternateProduction, err := NewProductionReceiptV2(fixture.productionInput, alternateProductionRef)
	if err != nil {
		t.Fatalf("create ProductionReceipt with alternate exact result: %v", err)
	}
	if alternateProduction.PayloadHash == fixture.production.PayloadHash {
		t.Fatal("ProductionReceipt payload hash did not commit to the exact controller result")
	}
}

func TestReleaseEvidenceRejectsMissingOrLegacyControllerResultReference(t *testing.T) {
	v2 := newReleaseEvidenceV2Fixture(t)
	legacy := newReleaseEvidenceV1Fixture(t, v2.bundle)

	missingV2 := []struct {
		name  string
		parse func() error
	}{
		{
			name: "PreviewReceipt",
			parse: func() error {
				value := v2.preview
				value.ControllerOperation = nil
				_, err := ParsePreviewReceipt(value)
				return err
			},
		},
		{
			name: "ProductionReceipt",
			parse: func() error {
				value := v2.production
				value.ControllerOperation = nil
				_, err := ParseProductionReceipt(value)
				return err
			},
		},
		{
			name: "DeploymentRevision",
			parse: func() error {
				value := v2.revision
				value.ControllerOperation = nil
				_, err := ParseDeploymentRevision(value)
				return err
			},
		},
	}
	for _, test := range missingV2 {
		t.Run("v2 "+test.name+" requires reference", func(t *testing.T) {
			if err := test.parse(); err == nil {
				t.Fatal("v2 evidence without a controller result reference was accepted")
			}
		})
	}

	if _, err := NewPreviewReceiptV2(v2.previewInput, ControllerOperationResultReference{}); err == nil {
		t.Fatal("NewPreviewReceiptV2 accepted an empty controller result reference")
	}
	if _, err := NewProductionReceiptV2(v2.productionInput, ControllerOperationResultReference{}); err == nil {
		t.Fatal("NewProductionReceiptV2 accepted an empty controller result reference")
	}
	if _, err := NewDeploymentRevisionV2(v2.revisionInput, ControllerOperationResultReference{}); err == nil {
		t.Fatal("NewDeploymentRevisionV2 accepted an empty controller result reference")
	}

	legacyReference := v2.productionRef
	legacyWithReference := []struct {
		name  string
		parse func() error
	}{
		{
			name: "PreviewReceipt",
			parse: func() error {
				value := legacy.preview
				value.ControllerOperation = &legacyReference
				_, err := ParsePreviewReceipt(value)
				return err
			},
		},
		{
			name: "ProductionReceipt",
			parse: func() error {
				value := legacy.production
				value.ControllerOperation = &legacyReference
				_, err := ParseProductionReceipt(value)
				return err
			},
		},
		{
			name: "DeploymentRevision",
			parse: func() error {
				value := legacy.revision
				value.ControllerOperation = &legacyReference
				_, err := ParseDeploymentRevision(value)
				return err
			},
		},
	}
	for _, test := range legacyWithReference {
		t.Run("v1 "+test.name+" rejects reference", func(t *testing.T) {
			if err := test.parse(); err == nil {
				t.Fatal("legacy evidence carrying a v2 controller result reference was accepted")
			}
		})
	}
}

func TestReleaseEvidenceRejectsV1V2GenerationDowngrade(t *testing.T) {
	v2 := newReleaseEvidenceV2Fixture(t)
	legacy := newReleaseEvidenceV1Fixture(t, v2.bundle)

	downgradedPreview := v2.preview
	downgradedPreview.SchemaVersion = PreviewReceiptSchemaVersion
	downgradedPreview.ControllerOperation = nil
	downgradedPreview = rehashReleasePreviewReceipt(t, downgradedPreview)
	if _, err := ParsePreviewReceipt(downgradedPreview); err != nil {
		t.Fatalf("prepare structurally valid downgraded PreviewReceipt: %v", err)
	}
	downgradedApproval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: downgradedPreview, Reason: "downgrade probe",
		CreatedBy: v2.bundle.CreatedBy, CreatedAt: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("prepare downgraded preview approval: %v", err)
	}
	downgradedProductionInput := v2.productionInput
	downgradedProductionInput.Preview = downgradedPreview
	downgradedProductionInput.Approval = downgradedApproval
	if _, err := NewProductionReceiptV2(downgradedProductionInput, v2.productionRef); err == nil {
		t.Fatal("ProductionReceipt v2 accepted a rehashed v1 PreviewReceipt downgrade")
	}

	upgradedPreview := legacy.preview
	upgradedPreview.SchemaVersion = PreviewReceiptSchemaVersionV2
	upgradedPreview.ControllerOperation = cloneControllerOperationResultReference(&v2.previewRef)
	upgradedPreview = rehashReleasePreviewReceipt(t, upgradedPreview)
	if _, err := ParsePreviewReceipt(upgradedPreview); err != nil {
		t.Fatalf("prepare structurally valid upgraded PreviewReceipt: %v", err)
	}
	upgradedApproval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: upgradedPreview, Reason: "upgrade probe",
		CreatedBy: legacy.bundle.CreatedBy, CreatedAt: time.Date(2026, 7, 18, 12, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("prepare upgraded preview approval: %v", err)
	}
	upgradedProductionInput := legacy.productionInput
	upgradedProductionInput.Preview = upgradedPreview
	upgradedProductionInput.Approval = upgradedApproval
	if _, err := NewProductionReceipt(upgradedProductionInput); err == nil {
		t.Fatal("ProductionReceipt v1 accepted a rehashed v2 PreviewReceipt generation")
	}

	downgradedProduction := v2.production
	downgradedProduction.SchemaVersion = ProductionReceiptSchemaVersion
	downgradedProduction.ControllerOperation = nil
	downgradedProduction = rehashReleaseProductionReceipt(t, downgradedProduction)
	if _, err := ParseProductionReceipt(downgradedProduction); err != nil {
		t.Fatalf("prepare structurally valid downgraded ProductionReceipt: %v", err)
	}
	downgradedRevisionInput := v2.revisionInput
	downgradedRevisionInput.Receipt = downgradedProduction
	if _, err := NewDeploymentRevisionV2(downgradedRevisionInput, v2.productionRef); err == nil {
		t.Fatal("DeploymentRevision v2 accepted a rehashed v1 ProductionReceipt downgrade")
	}

	upgradedProduction := legacy.production
	upgradedProduction.SchemaVersion = ProductionReceiptSchemaVersionV2
	upgradedProduction.ControllerOperation = cloneControllerOperationResultReference(&v2.productionRef)
	upgradedProduction = rehashReleaseProductionReceipt(t, upgradedProduction)
	if _, err := ParseProductionReceipt(upgradedProduction); err != nil {
		t.Fatalf("prepare structurally valid upgraded ProductionReceipt: %v", err)
	}
	upgradedRevisionInput := legacy.revisionInput
	upgradedRevisionInput.Receipt = upgradedProduction
	if _, err := NewDeploymentRevision(upgradedRevisionInput); err == nil {
		t.Fatal("DeploymentRevision v1 accepted a rehashed v2 ProductionReceipt generation")
	}
}

func TestDeploymentRevisionV2RequiresProductionReceiptControllerResult(t *testing.T) {
	fixture := newReleaseEvidenceV2Fixture(t)

	operationMismatch := fixture.productionRef
	operationMismatch.OperationID = uuid.NewString()
	if _, err := NewDeploymentRevisionV2(fixture.revisionInput, operationMismatch); err == nil {
		t.Fatal("DeploymentRevision v2 accepted a controller operation different from its ProductionReceipt")
	}

	resultMismatch := fixture.productionRef
	resultMismatch.ResultHash = releaseEvidenceDifferentHash(resultMismatch.ResultHash)
	if _, err := NewDeploymentRevisionV2(fixture.revisionInput, resultMismatch); err == nil {
		t.Fatal("DeploymentRevision v2 accepted a controller result different from its ProductionReceipt")
	}
}

func TestLegacyReleaseEvidenceV1RemainsReadable(t *testing.T) {
	legacy := newReleaseEvidenceV1Fixture(t, deploymentBundleFixture(t))
	if parsed, err := ParsePreviewReceipt(legacy.preview); err != nil || !reflect.DeepEqual(parsed, legacy.preview) {
		t.Fatalf("parse legacy PreviewReceipt: parsed=%+v err=%v", parsed, err)
	}
	if parsed, err := ParsePromotionApproval(legacy.approval); err != nil || !reflect.DeepEqual(parsed, legacy.approval) {
		t.Fatalf("parse legacy PromotionApproval: parsed=%+v err=%v", parsed, err)
	}
	if parsed, err := ParseProductionReceipt(legacy.production); err != nil || !reflect.DeepEqual(parsed, legacy.production) {
		t.Fatalf("parse legacy ProductionReceipt: parsed=%+v err=%v", parsed, err)
	}
	if parsed, err := ParseDeploymentRevision(legacy.revision); err != nil || !reflect.DeepEqual(parsed, legacy.revision) {
		t.Fatalf("parse legacy DeploymentRevision: parsed=%+v err=%v", parsed, err)
	}
	if legacy.preview.ControllerOperation != nil || legacy.production.ControllerOperation != nil || legacy.revision.ControllerOperation != nil {
		t.Fatal("legacy constructors unexpectedly emitted v2 controller result references")
	}
}

func newReleaseEvidenceV2Fixture(t *testing.T) releaseEvidenceV2Fixture {
	t.Helper()
	bundle := deploymentBundleFixture(t)
	actorID := bundle.CreatedBy
	previewResult := releaseControllerResultFixture(t, bundle, DeliveryOperationPreview)
	previewRef := ControllerOperationResultReference{
		OperationID: previewResult.OperationID,
		ResultHash:  previewResult.ResultHash,
	}
	previewInput := NewPreviewReceiptInput{
		ID: uuid.NewString(), RunID: previewResult.OperationID, Bundle: bundle,
		Namespace: "preview-project-release", Provider: previewResult.Provider,
		ProviderRef: previewResult.ProviderRef, Checks: passingReleasePreviewChecks(),
		Decision: PreviewPassed, CreatedBy: actorID,
		CreatedAt: time.Date(2026, 7, 18, 10, 30, 0, 123456000, time.UTC),
	}
	preview, err := NewPreviewReceiptV2(previewInput, previewRef)
	if err != nil {
		t.Fatalf("create PreviewReceipt v2: %v", err)
	}
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: preview, Reason: "Promote the exact controller-backed preview.",
		CreatedBy: actorID, CreatedAt: time.Date(2026, 7, 18, 10, 31, 0, 123456000, time.UTC),
	})
	if err != nil {
		t.Fatalf("create v2 preview approval: %v", err)
	}
	productionResult := releaseControllerResultFixture(t, bundle, DeliveryOperationProduction)
	productionRef := ControllerOperationResultReference{
		OperationID: productionResult.OperationID,
		ResultHash:  productionResult.ResultHash,
	}
	productionInput := NewProductionReceiptInput{
		ID: uuid.NewString(), RunID: productionResult.OperationID, Bundle: bundle, Preview: preview, Approval: approval,
		Operation: DeploymentPromote, Provider: productionResult.Provider, ProviderRef: productionResult.ProviderRef,
		PublicURL: productionResult.PublicURL, Checks: passingReleaseProductionChecks(), Decision: PreviewPassed,
		CreatedBy: actorID, CreatedAt: time.Date(2026, 7, 18, 10, 32, 0, 123456000, time.UTC),
	}
	production, err := NewProductionReceiptV2(productionInput, productionRef)
	if err != nil {
		t.Fatalf("create ProductionReceipt v2: %v", err)
	}
	revisionInput := NewDeploymentRevisionInput{
		ID: uuid.NewString(), RunID: productionInput.RunID, Bundle: bundle, Preview: preview,
		Approval: approval, Receipt: production, Operation: DeploymentPromote,
		Provider: productionInput.Provider, ProviderRef: productionInput.ProviderRef,
		PublicURL: productionInput.PublicURL, Checks: append([]PreviewCheck(nil), productionInput.Checks...),
		CreatedBy: actorID, CreatedAt: time.Date(2026, 7, 18, 10, 33, 0, 123456000, time.UTC),
	}
	revision, err := NewDeploymentRevisionV2(revisionInput, productionRef)
	if err != nil {
		t.Fatalf("create DeploymentRevision v2: %v", err)
	}
	return releaseEvidenceV2Fixture{
		bundle: bundle, previewInput: previewInput, previewRef: previewRef, preview: preview,
		approval: approval, productionInput: productionInput, productionRef: productionRef,
		production: production, revisionInput: revisionInput, revision: revision,
	}
}

func newReleaseEvidenceV1Fixture(t *testing.T, bundle Bundle) releaseEvidenceV1Fixture {
	t.Helper()
	actorID := bundle.CreatedBy
	previewInput := NewPreviewReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), Bundle: bundle,
		Namespace: "legacy-preview", Provider: "legacy-controller", ProviderRef: "preview/legacy",
		Checks: passingReleasePreviewChecks(), Decision: PreviewPassed, CreatedBy: actorID,
		CreatedAt: time.Date(2026, 7, 18, 11, 0, 0, 123456000, time.UTC),
	}
	preview, err := NewPreviewReceipt(previewInput)
	if err != nil {
		t.Fatalf("create legacy PreviewReceipt: %v", err)
	}
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: preview, Reason: "Preserve legacy evidence compatibility.",
		CreatedBy: actorID, CreatedAt: time.Date(2026, 7, 18, 11, 1, 0, 123456000, time.UTC),
	})
	if err != nil {
		t.Fatalf("create legacy approval: %v", err)
	}
	productionInput := NewProductionReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), Bundle: bundle, Preview: preview, Approval: approval,
		Operation: DeploymentPromote, Provider: "legacy-controller", ProviderRef: "production/legacy",
		PublicURL: "https://legacy.example.test", Checks: passingReleaseProductionChecks(), Decision: PreviewPassed,
		CreatedBy: actorID, CreatedAt: time.Date(2026, 7, 18, 11, 2, 0, 123456000, time.UTC),
	}
	production, err := NewProductionReceipt(productionInput)
	if err != nil {
		t.Fatalf("create legacy ProductionReceipt: %v", err)
	}
	revisionInput := NewDeploymentRevisionInput{
		ID: uuid.NewString(), RunID: productionInput.RunID, Bundle: bundle, Preview: preview,
		Approval: approval, Receipt: production, Operation: DeploymentPromote,
		Provider: productionInput.Provider, ProviderRef: productionInput.ProviderRef,
		PublicURL: productionInput.PublicURL, Checks: append([]PreviewCheck(nil), productionInput.Checks...),
		CreatedBy: actorID, CreatedAt: time.Date(2026, 7, 18, 11, 3, 0, 123456000, time.UTC),
	}
	revision, err := NewDeploymentRevision(revisionInput)
	if err != nil {
		t.Fatalf("create legacy DeploymentRevision: %v", err)
	}
	return releaseEvidenceV1Fixture{
		bundle: bundle, previewInput: previewInput, preview: preview, approval: approval,
		productionInput: productionInput, production: production, revisionInput: revisionInput, revision: revision,
	}
}

func releaseControllerResultFixture(t *testing.T, bundle Bundle, kind DeliveryOperationKind) DeliveryOperationResult {
	t.Helper()
	payload := map[string]any{
		"releaseBundle": repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
	}
	if kind == DeliveryOperationProduction {
		payload["expectedHead"] = ExpectedProductionHead{
			Revision: &repository.ExactReference{
				ID: "33333333-3333-4333-8333-333333333333", ContentHash: deliveryHashA,
			},
			ProductionReceipt: &repository.ExactReference{
				ID: "44444444-4444-4444-8444-444444444444", ContentHash: deliveryHashB,
			},
		}
	}
	request, err := NewDeliveryOperationRequest(uuid.NewString(), kind, bundle.ProjectID, payload)
	if err != nil {
		t.Fatalf("create delivery operation request: %v", err)
	}
	result := completedDeliveryOperationResultFixture(t, request)
	parsed, err := ParseDeliveryOperationResult(result, deliveryControllerIdentityFixture(), request)
	if err != nil {
		t.Fatalf("parse delivery operation result fixture: %v", err)
	}
	return parsed
}

func rehashReleasePreviewReceipt(t *testing.T, value PreviewReceipt) PreviewReceipt {
	t.Helper()
	value.PayloadHash = ""
	hash, err := domain.CanonicalHash(previewReceiptHashPayload(value))
	if err != nil {
		t.Fatalf("rehash PreviewReceipt: %v", err)
	}
	value.PayloadHash = "sha256:" + hash
	return value
}

func rehashReleaseProductionReceipt(t *testing.T, value ProductionReceipt) ProductionReceipt {
	t.Helper()
	value.PayloadHash = ""
	hash, err := domain.CanonicalHash(productionReceiptHashPayload(value))
	if err != nil {
		t.Fatalf("rehash ProductionReceipt: %v", err)
	}
	value.PayloadHash = "sha256:" + hash
	return value
}

func releaseEvidenceDifferentHash(value string) string {
	last := byte('0')
	if value[len(value)-1] == last {
		last = '1'
	}
	return value[:len(value)-1] + string(last)
}
