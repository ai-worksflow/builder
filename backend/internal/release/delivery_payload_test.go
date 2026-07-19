package release

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

func TestPreviewDeliveryOperationPayloadPersistsCompleteBundleAndInputs(t *testing.T) {
	bundle := deploymentBundleFixture(t)
	runID := uuid.NewString()
	request, err := newPreviewDeliveryOperationRequest(
		runID,
		bundle,
		"  exercise the exact reviewed release  ",
		"  preview-project-release  ",
	)
	if err != nil {
		t.Fatalf("create preview delivery operation request: %v", err)
	}
	payload := decodeDeliveryOperationPayload[PreviewDeliveryOperationPayload](t, request)

	if payload.SchemaVersion != PreviewDeliveryOperationPayloadSchema ||
		payload.OperationID != runID || payload.RunID != runID ||
		payload.ProjectID != bundle.ProjectID ||
		payload.Reason != "exercise the exact reviewed release" ||
		payload.Namespace != "preview-project-release" ||
		!reflect.DeepEqual(payload.ReleaseBundle, bundle) {
		t.Fatalf("preview controller payload lost an immutable input: %+v", payload)
	}
	if request.Kind != DeliveryOperationPreview || request.ProjectID != bundle.ProjectID {
		t.Fatalf("preview request lineage drifted: %+v", request)
	}
}

func TestProductionDeliveryOperationPayloadPersistsPromotionAndRollbackDocuments(t *testing.T) {
	expected := deliveryPayloadExpectedHead()

	t.Run("promotion contains full reviewed documents and explicit null source", func(t *testing.T) {
		service, store, _ := deliveryServiceFixture(t)
		actorID, projectID := store.bundle.CreatedBy, store.bundle.ProjectID
		_, replayed, err := service.StartPromotion(context.Background(), StartPromotionRequest{
			ProjectID: projectID, PromotionApprovalID: store.approval.ID,
			PromotionApprovalHash: store.approval.PayloadHash,
			Reason:                "  promote exact reviewed documents  ", ActorID: actorID, OperationID: "promote-complete-payload",
		})
		if err != nil || replayed {
			t.Fatalf("start promotion: replayed=%v err=%v", replayed, err)
		}
		input := store.productionInput
		request, err := newProductionDeliveryOperationRequest(
			input.ID, input.Environment, input.Reason, input.Operation,
			input.BundleDocument, input.PreviewDocument, input.ApprovalDocument,
			input.SourceDocument, expected,
		)
		if err != nil {
			t.Fatalf("create promotion controller request: %v", err)
		}
		payload := decodeDeliveryOperationPayload[ProductionDeliveryOperationPayload](t, request)
		assertCompleteProductionPayload(t, payload, input, expected)
		if payload.SourceRevision != nil || !strings.Contains(string(request.RequestDocument), `"sourceRevision":null`) {
			t.Fatalf("promotion did not bind an explicit null source revision: %s", request.RequestDocument)
		}
		if request.RequestHash == input.RequestHash {
			t.Fatalf("controller operation hash reused API idempotency hash %s", request.RequestHash)
		}
	})

	t.Run("rollback contains full reviewed documents and source revision", func(t *testing.T) {
		service, store, _ := deliveryServiceFixture(t)
		actorID, projectID := store.bundle.CreatedBy, store.bundle.ProjectID
		sourceRef, err := store.revision.ExactReference()
		if err != nil {
			t.Fatal(err)
		}
		_, replayed, err := service.StartRollback(context.Background(), StartRollbackRequest{
			ProjectID: projectID, SourceRevisionID: sourceRef.ID, SourceRevisionHash: sourceRef.ContentHash,
			Reason: "  restore the exact immutable revision  ", ActorID: actorID, OperationID: "rollback-complete-payload",
		})
		if err != nil || replayed {
			t.Fatalf("start rollback: replayed=%v err=%v", replayed, err)
		}
		input := store.productionInput
		request, err := newProductionDeliveryOperationRequest(
			input.ID, input.Environment, input.Reason, input.Operation,
			input.BundleDocument, input.PreviewDocument, input.ApprovalDocument,
			input.SourceDocument, expected,
		)
		if err != nil {
			t.Fatalf("create rollback controller request: %v", err)
		}
		payload := decodeDeliveryOperationPayload[ProductionDeliveryOperationPayload](t, request)
		assertCompleteProductionPayload(t, payload, input, expected)
		if payload.SourceRevision == nil || !reflect.DeepEqual(*payload.SourceRevision, store.revision) {
			t.Fatalf("rollback controller payload lost its exact source revision: %+v", payload.SourceRevision)
		}
		if request.RequestHash == input.RequestHash {
			t.Fatalf("controller operation hash reused API idempotency hash %s", request.RequestHash)
		}
	})
}

func TestProductionDeliveryOperationPayloadRejectsLineageAndExpectedHeadDrift(t *testing.T) {
	_, store, _ := deliveryServiceFixture(t)
	valid := func() (Bundle, PreviewReceipt, PromotionApproval, *DeploymentRevision) {
		source := store.revision
		return store.bundle, store.preview, store.approval, &source
	}
	create := func(
		operation DeploymentOperation,
		bundle Bundle,
		preview PreviewReceipt,
		approval PromotionApproval,
		source *DeploymentRevision,
		expected ExpectedProductionHead,
	) error {
		_, err := newProductionDeliveryOperationRequest(
			uuid.NewString(), "production", "lineage rejection probe", operation,
			bundle, preview, approval, source, expected,
		)
		return err
	}

	otherBundle := deploymentBundleFixture(t)
	otherPreview, otherApproval := deliveryPayloadPreviewApproval(t, otherBundle)
	alternatePreview, alternateApproval := deliveryPayloadPreviewApproval(t, store.bundle)
	otherSource := deliveryServiceSourceFixture(t, otherBundle, otherPreview, otherApproval)
	validBundle, validPreview, validApproval, validSource := valid()
	expected := deliveryPayloadExpectedHead()

	tests := []struct {
		name      string
		operation DeploymentOperation
		bundle    Bundle
		preview   PreviewReceipt
		approval  PromotionApproval
		source    *DeploymentRevision
		expected  ExpectedProductionHead
	}{
		{
			name: "bundle and reviewed preview", operation: DeploymentPromote,
			bundle: otherBundle, preview: validPreview, approval: validApproval, expected: expected,
		},
		{
			name: "preview and approval", operation: DeploymentPromote,
			bundle: validBundle, preview: validPreview, approval: alternateApproval, expected: expected,
		},
		{
			name: "rollback source", operation: DeploymentRollback,
			bundle: validBundle, preview: validPreview, approval: validApproval, source: &otherSource, expected: expected,
		},
		{
			name: "expected head missing receipt", operation: DeploymentPromote,
			bundle: validBundle, preview: validPreview, approval: validApproval,
			expected: ExpectedProductionHead{Revision: expected.Revision},
		},
		{
			name: "expected head missing revision", operation: DeploymentPromote,
			bundle: validBundle, preview: validPreview, approval: validApproval,
			expected: ExpectedProductionHead{ProductionReceipt: expected.ProductionReceipt},
		},
		{
			name: "promotion source must be null", operation: DeploymentPromote,
			bundle: validBundle, preview: validPreview, approval: validApproval, source: validSource, expected: expected,
		},
		{
			name: "rollback source is required", operation: DeploymentRollback,
			bundle: validBundle, preview: validPreview, approval: validApproval, expected: expected,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := create(
				test.operation, test.bundle, test.preview, test.approval, test.source, test.expected,
			); err == nil {
				t.Fatal("mismatched production controller lineage was accepted")
			}
		})
	}

	// The alternate pair is valid in isolation; this assertion makes sure the
	// preview/approval mismatch above is testing lineage, not malformed fixtures.
	if err := create(
		DeploymentPromote, validBundle, alternatePreview, alternateApproval, nil, expected,
	); err != nil {
		t.Fatalf("valid alternate reviewed lineage was rejected: %v", err)
	}
}

func TestDeliveryServicePassesCompleteImmutableDocumentsToStore(t *testing.T) {
	t.Run("preview", func(t *testing.T) {
		service, store, _ := deliveryServiceFixture(t)
		_, _, err := service.StartPreview(context.Background(), StartPreviewRequest{
			ProjectID: store.bundle.ProjectID, ReleaseBundleID: store.bundle.ID,
			ReleaseBundleHash: store.bundle.BundleHash, Reason: "  preview exact bundle  ",
			ActorID: store.bundle.CreatedBy, OperationID: "preview-documents",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(store.previewInput.BundleDocument, store.bundle) ||
			store.previewInput.ReleaseBundle != store.bundleReference() ||
			store.previewInput.Reason != "preview exact bundle" {
			t.Fatalf("DeliveryService did not pass the complete preview Bundle: %+v", store.previewInput)
		}
	})

	t.Run("promotion", func(t *testing.T) {
		service, store, _ := deliveryServiceFixture(t)
		_, _, err := service.StartPromotion(context.Background(), StartPromotionRequest{
			ProjectID: store.bundle.ProjectID, PromotionApprovalID: store.approval.ID,
			PromotionApprovalHash: store.approval.PayloadHash, Reason: "promote exact documents",
			ActorID: store.bundle.CreatedBy, OperationID: "promotion-documents",
		})
		if err != nil {
			t.Fatal(err)
		}
		input := store.productionInput
		if !reflect.DeepEqual(input.BundleDocument, store.bundle) ||
			!reflect.DeepEqual(input.PreviewDocument, store.preview) ||
			!reflect.DeepEqual(input.ApprovalDocument, store.approval) || input.SourceDocument != nil {
			t.Fatalf("DeliveryService did not pass complete promotion documents: %+v", input)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		service, store, _ := deliveryServiceFixture(t)
		sourceRef, err := store.revision.ExactReference()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = service.StartRollback(context.Background(), StartRollbackRequest{
			ProjectID: store.bundle.ProjectID, SourceRevisionID: sourceRef.ID,
			SourceRevisionHash: sourceRef.ContentHash, Reason: "rollback exact documents",
			ActorID: store.bundle.CreatedBy, OperationID: "rollback-documents",
		})
		if err != nil {
			t.Fatal(err)
		}
		input := store.productionInput
		if !reflect.DeepEqual(input.BundleDocument, store.bundle) ||
			!reflect.DeepEqual(input.PreviewDocument, store.preview) ||
			!reflect.DeepEqual(input.ApprovalDocument, store.approval) ||
			input.SourceDocument == nil || !reflect.DeepEqual(*input.SourceDocument, store.revision) {
			t.Fatalf("DeliveryService did not pass complete rollback documents: %+v", input)
		}
	})
}

func decodeDeliveryOperationPayload[T any](t *testing.T, request DeliveryOperationRequest) T {
	t.Helper()
	parsed, err := ParseDeliveryOperationRequest(request)
	if err != nil {
		t.Fatalf("parse delivery operation request: %v", err)
	}
	var document deliveryOperationDocument
	if err := decodeReleaseStrictJSON(parsed.RequestDocument, &document); err != nil {
		t.Fatalf("decode delivery operation document: %v", err)
	}
	var payload T
	if err := decodeReleaseStrictJSON(document.Payload, &payload); err != nil {
		t.Fatalf("decode delivery operation payload: %v", err)
	}
	return payload
}

func assertCompleteProductionPayload(
	t *testing.T,
	payload ProductionDeliveryOperationPayload,
	input CreateProductionRunInput,
	expected ExpectedProductionHead,
) {
	t.Helper()
	if payload.SchemaVersion != ProductionDeliveryOperationPayloadSchema ||
		payload.OperationID != input.ID || payload.RunID != input.ID ||
		payload.ProjectID != input.ProjectID || payload.Environment != input.Environment ||
		payload.Operation != input.Operation || payload.Reason != input.Reason ||
		!reflect.DeepEqual(payload.ReleaseBundle, input.BundleDocument) ||
		!reflect.DeepEqual(payload.PreviewReceipt, input.PreviewDocument) ||
		!reflect.DeepEqual(payload.PromotionApproval, input.ApprovalDocument) ||
		!reflect.DeepEqual(payload.ExpectedHead, expected) {
		t.Fatalf("production controller payload lost an immutable input: %+v", payload)
	}
}

func deliveryPayloadExpectedHead() ExpectedProductionHead {
	return ExpectedProductionHead{
		Revision: &repository.ExactReference{
			ID: "33333333-3333-4333-8333-333333333333", ContentHash: deliveryHashA,
		},
		ProductionReceipt: &repository.ExactReference{
			ID: "44444444-4444-4444-8444-444444444444", ContentHash: deliveryHashB,
		},
	}
}

func deliveryPayloadPreviewApproval(t *testing.T, bundle Bundle) (PreviewReceipt, PromotionApproval) {
	t.Helper()
	previewController := ControllerOperationResultReference{
		OperationID: uuid.NewString(), ResultHash: deliveryHashA,
	}
	preview, err := NewPreviewReceiptV2(NewPreviewReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), Bundle: bundle,
		Namespace: "preview-lineage-probe", Provider: "fake", ProviderRef: "preview/lineage-probe",
		Checks: passingReleasePreviewChecks(), Decision: PreviewPassed, CreatedBy: bundle.CreatedBy,
		CreatedAt: time.Date(2026, 7, 18, 14, 0, 0, 123456000, time.UTC),
	}, previewController)
	if err != nil {
		t.Fatalf("create lineage PreviewReceipt: %v", err)
	}
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: preview, Reason: "lineage probe",
		CreatedBy: bundle.CreatedBy, CreatedAt: time.Date(2026, 7, 18, 14, 1, 0, 123456000, time.UTC),
	})
	if err != nil {
		t.Fatalf("create lineage PromotionApproval: %v", err)
	}
	return preview, approval
}

func deliveryServiceSourceFixture(
	t *testing.T,
	bundle Bundle,
	preview PreviewReceipt,
	approval PromotionApproval,
) DeploymentRevision {
	t.Helper()
	runID := uuid.NewString()
	productionController := ControllerOperationResultReference{
		OperationID: runID, ResultHash: deliveryHashB,
	}
	receipt, err := NewProductionReceiptV2(NewProductionReceiptInput{
		ID: uuid.NewString(), RunID: runID, Bundle: bundle, Preview: preview, Approval: approval,
		Operation: DeploymentPromote, Provider: "fake", ProviderRef: "production/lineage-probe",
		PublicURL: "https://lineage-probe.example.test", Checks: passingReleaseProductionChecks(),
		Decision: PreviewPassed, CreatedBy: bundle.CreatedBy,
		CreatedAt: time.Date(2026, 7, 18, 14, 2, 0, 123456000, time.UTC),
	}, productionController)
	if err != nil {
		t.Fatalf("create lineage ProductionReceipt: %v", err)
	}
	revision, err := NewDeploymentRevisionV2(NewDeploymentRevisionInput{
		ID: uuid.NewString(), RunID: runID, Bundle: bundle, Preview: preview, Approval: approval,
		Receipt: receipt, Operation: DeploymentPromote, Provider: "fake",
		ProviderRef: "production/lineage-probe", PublicURL: "https://lineage-probe.example.test",
		Checks: passingReleaseProductionChecks(), CreatedBy: bundle.CreatedBy,
		CreatedAt: time.Date(2026, 7, 18, 14, 3, 0, 123456000, time.UTC),
	}, productionController)
	if err != nil {
		t.Fatalf("create lineage DeploymentRevision: %v", err)
	}
	return revision
}
