package transport

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/release"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	releaseTransportActor   = "30000000-0000-4000-8000-000000000001"
	releaseTransportProject = "30000000-0000-4000-8000-000000000002"
	releaseTransportBundle  = "30000000-0000-4000-8000-000000000003"
	releaseTransportPreview = "30000000-0000-4000-8000-000000000004"
	releaseTransportApprove = "30000000-0000-4000-8000-000000000005"
	releaseTransportDeploy  = "30000000-0000-4000-8000-000000000006"
	releaseTransportSource  = "30000000-0000-4000-8000-000000000007"
)

const releaseTransportHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type releaseAPIFake struct{}

func (releaseAPIFake) CreateBundle(context.Context, release.CreateBundleRequest) (release.BundleView, error) {
	return release.BundleView{}, nil
}
func (releaseAPIFake) GetBundle(context.Context, string, string, string, string) (release.Bundle, error) {
	return release.Bundle{}, nil
}
func (releaseAPIFake) GetBundleByReceipt(context.Context, string, string, string, string) (release.Bundle, error) {
	return release.Bundle{}, nil
}

type releaseDeliveryAPIFake struct {
	previewRequest        release.StartPreviewRequest
	approvalRequest       release.ApprovePromotionRequest
	promotionRequest      release.StartPromotionRequest
	rollbackRequest       release.StartRollbackRequest
	previewErr            error
	productionErr         error
	reconciliationRequest release.ResumeBlockedDeliveryRequest
	reconciliationErr     error
}

func (fake *releaseDeliveryAPIFake) ResumeBlockedDelivery(
	_ context.Context,
	request release.ResumeBlockedDeliveryRequest,
) (release.DeliveryReconciliationCase, bool, error) {
	fake.reconciliationRequest = request
	if fake.reconciliationErr != nil {
		return release.DeliveryReconciliationCase{}, false, fake.reconciliationErr
	}
	return release.DeliveryReconciliationCase{
		SchemaVersion: release.DeliveryReconciliationCaseSchemaVersion,
		ID:            releaseTransportDeploy, ProjectID: request.ProjectID, RunKind: request.RunKind,
		RunID: request.RunID, ExpectedRunVersion: request.ExpectedVersion,
		CaseHash: releaseTransportHash,
	}, false, nil
}

func (*releaseDeliveryAPIFake) GetDeliveryReconciliationCase(
	context.Context, string, string, string,
) (release.DeliveryReconciliationCase, error) {
	return release.DeliveryReconciliationCase{ID: releaseTransportDeploy, ProjectID: releaseTransportProject, CaseHash: releaseTransportHash}, nil
}

func (*releaseDeliveryAPIFake) ListDeliveryReconciliationCases(
	context.Context, string, string,
) ([]release.DeliveryReconciliationCase, error) {
	return []release.DeliveryReconciliationCase{}, nil
}

func (fake *releaseDeliveryAPIFake) GetBlockedDeliveryReconciliationSnapshot(
	_ context.Context,
	projectID string,
	runKind release.DeliveryOperationKind,
	runID, _ string,
) (release.DeliveryReconciliationBlockSnapshot, error) {
	if fake.reconciliationErr != nil {
		return release.DeliveryReconciliationBlockSnapshot{}, fake.reconciliationErr
	}
	return release.DeliveryReconciliationBlockSnapshot{
		SchemaVersion: "release-delivery-reconciliation-block/v1",
		ProjectID:     projectID, RunKind: runKind, RunID: runID,
		RunSchemaVersion: "release-preview-run/v2", ExpectedRunVersion: 4,
		OperationID: runID, OperationRequestHash: releaseTransportHash,
		LastError: release.DeliveryReconciliationError{
			Code: "controller-authority-conflict", Detail: "pinned controller was unavailable",
		},
	}, nil
}

func TestReleaseTransportMapsLegacyBlockedSnapshotFailClosed(t *testing.T) {
	router := releaseTransportRouter(t, &releaseDeliveryAPIFake{
		reconciliationErr: release.ErrDeliveryReconciliationLegacy,
	})
	request := httptest.NewRequest(http.MethodGet,
		"/projects/"+releaseTransportProject+"/release-delivery-reconciliation-blocks/preview/"+releaseTransportPreview, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"code":"legacy_release_delivery_not_recoverable"`)) {
		t.Fatalf("legacy snapshot status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestReleaseTransportReadsBlockedSnapshotAndPostsExactCAS(t *testing.T) {
	delivery := &releaseDeliveryAPIFake{}
	router := releaseTransportRouter(t, delivery)

	read := httptest.NewRequest(http.MethodGet,
		"/projects/"+releaseTransportProject+"/release-delivery-reconciliation-blocks/preview/"+releaseTransportPreview, nil)
	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK ||
		!bytes.Contains(readResponse.Body.Bytes(), []byte(`"expectedRunVersion":4`)) ||
		!bytes.Contains(readResponse.Body.Bytes(), []byte(`"code":"controller-authority-conflict"`)) {
		t.Fatalf("blocked snapshot status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}

	resume := httptest.NewRequest(http.MethodPost,
		"/projects/"+releaseTransportProject+"/release-delivery-reconciliation-cases",
		bytes.NewBufferString(`{"runKind":"preview","runId":"`+releaseTransportPreview+`","expectedVersion":4,"expectedErrorCode":"controller-authority-conflict","reason":"controller history repaired"}`))
	resume.Header.Set("Content-Type", "application/json")
	resume.Header.Set("Idempotency-Key", "resume-exact-operation")
	resumeResponse := httptest.NewRecorder()
	router.ServeHTTP(resumeResponse, resume)
	if resumeResponse.Code != http.StatusAccepted {
		t.Fatalf("resume status=%d body=%s", resumeResponse.Code, resumeResponse.Body.String())
	}
	if delivery.reconciliationRequest.ExpectedVersion != 4 ||
		delivery.reconciliationRequest.ExpectedErrorCode != "controller-authority-conflict" ||
		delivery.reconciliationRequest.OperationID != "resume-exact-operation" {
		t.Fatalf("resume request=%+v", delivery.reconciliationRequest)
	}
}

func (fake *releaseDeliveryAPIFake) StartPreview(_ context.Context, request release.StartPreviewRequest) (release.PreviewRun, bool, error) {
	fake.previewRequest = request
	if fake.previewErr != nil {
		return release.PreviewRun{}, false, fake.previewErr
	}
	return release.PreviewRun{
		ID: releaseTransportPreview, ProjectID: request.ProjectID,
		ReleaseBundle: repository.ExactReference{ID: request.ReleaseBundleID, ContentHash: request.ReleaseBundleHash},
		Reason:        request.Reason, State: release.DeliveryQueued, Version: 1, CreatedBy: request.ActorID,
	}, false, nil
}

func TestReleaseTransportMapsPreviewRunConflict(t *testing.T) {
	router := releaseTransportRouter(t, &releaseDeliveryAPIFake{previewErr: release.ErrPreviewRunConflict})
	request := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+releaseTransportProject+"/release-preview-runs",
		bytes.NewBufferString(`{"releaseBundle":{"id":"`+releaseTransportBundle+`","contentHash":"`+releaseTransportHash+`"},"reason":"concurrent preview"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "concurrent-preview")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"code":"release_preview_run_conflict"`)) {
		t.Fatalf("preview Run conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func (fake *releaseDeliveryAPIFake) ApprovePromotion(_ context.Context, request release.ApprovePromotionRequest) (release.PromotionApproval, bool, error) {
	fake.approvalRequest = request
	return release.PromotionApproval{
		ID: releaseTransportApprove, ProjectID: request.ProjectID,
		PreviewReceipt: repository.ExactReference{ID: request.PreviewReceiptID, ContentHash: request.PreviewReceiptHash},
		PayloadHash:    releaseTransportHash, CreatedBy: request.ActorID,
	}, false, nil
}

func (fake *releaseDeliveryAPIFake) StartPromotion(_ context.Context, request release.StartPromotionRequest) (release.ProductionRun, bool, error) {
	fake.promotionRequest = request
	if fake.productionErr != nil {
		return release.ProductionRun{}, false, fake.productionErr
	}
	return release.ProductionRun{
		ID: releaseTransportDeploy, ProjectID: request.ProjectID, Operation: release.DeploymentPromote,
		PromotionApproval: repository.ExactReference{ID: request.PromotionApprovalID, ContentHash: request.PromotionApprovalHash},
		Reason:            request.Reason, State: release.DeliveryQueued, Version: 1, CreatedBy: request.ActorID,
	}, false, nil
}

func TestReleaseTransportMapsProductionHeadConflict(t *testing.T) {
	router := releaseTransportRouter(t, &releaseDeliveryAPIFake{productionErr: release.ErrProductionHeadConflict})
	request := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+releaseTransportProject+"/release-deployment-runs/promote",
		bytes.NewBufferString(`{"promotionApproval":{"id":"`+releaseTransportApprove+`","contentHash":"`+releaseTransportHash+`"},"reason":"concurrent promotion"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "concurrent-promotion")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"code":"release_production_head_conflict"`)) {
		t.Fatalf("production head conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func (fake *releaseDeliveryAPIFake) StartRollback(_ context.Context, request release.StartRollbackRequest) (release.ProductionRun, bool, error) {
	fake.rollbackRequest = request
	return release.ProductionRun{
		ID: releaseTransportDeploy, ProjectID: request.ProjectID, Operation: release.DeploymentRollback,
		SourceRevision: &repository.ExactReference{ID: request.SourceRevisionID, ContentHash: request.SourceRevisionHash},
		Reason:         request.Reason, State: release.DeliveryQueued, Version: 1, CreatedBy: request.ActorID,
	}, false, nil
}

func (*releaseDeliveryAPIFake) GetPreviewRun(context.Context, string, string, string) (release.PreviewRun, error) {
	return release.PreviewRun{ID: releaseTransportPreview, ProjectID: releaseTransportProject, State: release.DeliveryPassed, Version: 4}, nil
}

func (*releaseDeliveryAPIFake) ListPreviewRuns(context.Context, string, string, string, string) ([]release.PreviewRun, error) {
	return []release.PreviewRun{}, nil
}

func (*releaseDeliveryAPIFake) GetPreviewReceipt(context.Context, string, string, string, string) (release.PreviewReceipt, error) {
	return release.PreviewReceipt{
		ID: releaseTransportPreview, ProjectID: releaseTransportProject, PayloadHash: releaseTransportHash,
		Decision: release.PreviewPassed,
	}, nil
}

func (*releaseDeliveryAPIFake) GetPromotionApprovalByPreview(context.Context, string, string, string, string) (release.PromotionApproval, error) {
	return release.PromotionApproval{}, nil
}

func (*releaseDeliveryAPIFake) GetProductionRun(context.Context, string, string, string) (release.ProductionRun, error) {
	return release.ProductionRun{ID: releaseTransportDeploy, ProjectID: releaseTransportProject, State: release.DeliveryHealthy, Version: 4}, nil
}

func (*releaseDeliveryAPIFake) ListProductionRuns(context.Context, string, string, string, string) ([]release.ProductionRun, error) {
	return []release.ProductionRun{}, nil
}

func (*releaseDeliveryAPIFake) ListProductionHistory(context.Context, string, string) ([]release.ProductionRun, error) {
	return []release.ProductionRun{}, nil
}

func (*releaseDeliveryAPIFake) GetProductionReceipt(context.Context, string, string, string, string) (release.ProductionReceipt, error) {
	return release.ProductionReceipt{
		ID: releaseTransportDeploy, ProjectID: releaseTransportProject, PayloadHash: releaseTransportHash,
		Decision: release.PreviewFailed,
	}, nil
}

func (*releaseDeliveryAPIFake) GetDeploymentRevision(context.Context, string, string, string, string) (release.DeploymentRevision, error) {
	return release.DeploymentRevision{
		ID: releaseTransportSource, ProjectID: releaseTransportProject, PayloadHash: releaseTransportHash,
	}, nil
}

func TestReleaseTransportCarriesExactPreviewPromotionAndRollbackAuthority(t *testing.T) {
	fake := &releaseDeliveryAPIFake{}
	router := releaseTransportRouter(t, fake)
	tests := []struct {
		path   string
		body   string
		key    string
		status int
	}{
		{
			path:   "/projects/" + releaseTransportProject + "/release-preview-runs",
			body:   `{"releaseBundle":{"id":"` + releaseTransportBundle + `","contentHash":"` + releaseTransportHash + `"},"reason":"isolated preview"}`,
			key:    "preview-operation",
			status: http.StatusAccepted,
		},
		{
			path:   "/projects/" + releaseTransportProject + "/release-promotion-approvals",
			body:   `{"previewReceipt":{"id":"` + releaseTransportPreview + `","contentHash":"` + releaseTransportHash + `"},"reason":"approve exact preview"}`,
			key:    "approval-operation",
			status: http.StatusCreated,
		},
		{
			path:   "/projects/" + releaseTransportProject + "/release-deployment-runs/promote",
			body:   `{"promotionApproval":{"id":"` + releaseTransportApprove + `","contentHash":"` + releaseTransportHash + `"},"reason":"promote without rebuild"}`,
			key:    "promotion-operation",
			status: http.StatusAccepted,
		},
		{
			path:   "/projects/" + releaseTransportProject + "/release-deployment-runs/rollback",
			body:   `{"sourceRevision":{"id":"` + releaseTransportSource + `","contentHash":"` + releaseTransportHash + `"},"reason":"rollback exact Bundle"}`,
			key:    "rollback-operation",
			status: http.StatusAccepted,
		},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", test.key)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("POST %s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
	if fake.previewRequest.ReleaseBundleID != releaseTransportBundle || fake.previewRequest.ReleaseBundleHash != releaseTransportHash ||
		fake.previewRequest.ActorID != releaseTransportActor || fake.previewRequest.OperationID != "preview-operation" {
		t.Fatalf("preview request lost exact authority: %+v", fake.previewRequest)
	}
	if fake.approvalRequest.PreviewReceiptID != releaseTransportPreview || fake.approvalRequest.OperationID != "approval-operation" {
		t.Fatalf("approval request lost exact PreviewReceipt: %+v", fake.approvalRequest)
	}
	if fake.promotionRequest.PromotionApprovalID != releaseTransportApprove || fake.promotionRequest.OperationID != "promotion-operation" {
		t.Fatalf("promotion request lost exact Approval: %+v", fake.promotionRequest)
	}
	if fake.rollbackRequest.SourceRevisionID != releaseTransportSource || fake.rollbackRequest.OperationID != "rollback-operation" {
		t.Fatalf("rollback request lost exact source revision: %+v", fake.rollbackRequest)
	}
}

func TestReleaseTransportExposesImmutableProductionFacts(t *testing.T) {
	router := releaseTransportRouter(t, &releaseDeliveryAPIFake{})
	for _, path := range []string{
		"/projects/" + releaseTransportProject + "/release-preview-receipts/" + releaseTransportPreview + "?receiptHash=" + releaseTransportHash,
		"/projects/" + releaseTransportProject + "/release-production-receipts/" + releaseTransportDeploy + "?receiptHash=" + releaseTransportHash,
		"/projects/" + releaseTransportProject + "/release-deployment-revisions/" + releaseTransportSource + "?revisionHash=" + releaseTransportHash,
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") == "" {
			t.Fatalf("GET %s status=%d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestReleaseTransportKeepsImmutableFactsReadableWhenMutationsAreDisabled(t *testing.T) {
	reader := &releaseDeliveryAPIFake{}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session: auth.Session{User: auth.User{ID: releaseTransportActor}}, Transport: "bearer",
		})
		c.Next()
	})
	handler, err := NewReleaseHandler(ReleaseDependencies{
		Service: releaseAPIFake{}, DeliveryRead: reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterReleaseRoutes(router, handler, worksmiddleware.CaptureIdempotencyKey(true)); err != nil {
		t.Fatal(err)
	}

	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest(
		http.MethodGet,
		"/projects/"+releaseTransportProject+"/release-deployment-revisions/"+releaseTransportSource+"?revisionHash="+releaseTransportHash,
		nil,
	))
	if read.Code != http.StatusOK {
		t.Fatalf("immutable revision read status=%d body=%s", read.Code, read.Body.String())
	}

	mutation := httptest.NewRecorder()
	router.ServeHTTP(mutation, httptest.NewRequest(
		http.MethodPost, "/projects/"+releaseTransportProject+"/release-preview-runs", bytes.NewBufferString(`{}`),
	))
	if mutation.Code != http.StatusNotFound {
		t.Fatalf("disabled mutation status=%d body=%s", mutation.Code, mutation.Body.String())
	}

	capabilities := httptest.NewRecorder()
	router.ServeHTTP(capabilities, httptest.NewRequest(
		http.MethodGet, "/projects/"+releaseTransportProject+"/release-capabilities", nil,
	))
	if capabilities.Code != http.StatusOK ||
		!bytes.Contains(capabilities.Body.Bytes(), []byte(`"deliveryEnabled":false`)) {
		t.Fatalf("disabled delivery capabilities status=%d body=%s", capabilities.Code, capabilities.Body.String())
	}
}

func releaseTransportRouter(t *testing.T, delivery ReleaseDeliveryAPI) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session: auth.Session{User: auth.User{ID: releaseTransportActor}}, Transport: "bearer",
		})
		c.Next()
	})
	handler, err := NewReleaseHandler(ReleaseDependencies{Service: releaseAPIFake{}, Delivery: delivery})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterReleaseRoutes(router, handler, worksmiddleware.CaptureIdempotencyKey(true)); err != nil {
		t.Fatal(err)
	}
	return router
}
