package release

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

func TestHTTPDeliveryProviderSendsOnlyExactBundleAuthorityWithIdempotency(t *testing.T) {
	bundle := deploymentBundleFixture(t)
	var seenPath, seenAuthorization, seenOperationID string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenPath = request.URL.Path
		seenAuthorization = request.Header.Get("Authorization")
		seenOperationID = request.Header.Get("Idempotency-Key")
		var payload struct {
			SchemaVersion string `json:"schemaVersion"`
			RunID         string `json:"runId"`
			Bundle        Bundle `json:"releaseBundle"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.SchemaVersion != "release-preview-controller-request/v1" || payload.Bundle.BundleHash != bundle.BundleHash {
			t.Errorf("controller request lost exact Bundle: %+v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"provider":"controller","providerRef":"namespace/preview","checks":[{"id":"contract","kind":"contract","status":"passed"},{"id":"health","kind":"health","status":"passed"},{"id":"migration","kind":"migration","status":"passed"},{"id":"playwright","kind":"e2e","status":"passed"},{"id":"smoke","kind":"smoke","status":"passed"}]}`))
	}))
	defer server.Close()
	provider, err := NewHTTPDeliveryProvider(HTTPDeliveryProviderConfig{
		BaseURL: server.URL, BearerToken: "0123456789abcdef0123456789abcdef",
		RequestTimeout: time.Minute, MaxResponseBytes: 1 << 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runID := "11111111-1111-4111-8111-111111111111"
	result, err := provider.Preview(context.Background(), PreviewProviderRequest{
		RunID: runID, ProjectID: bundle.ProjectID, Namespace: "preview", Bundle: bundle,
	})
	if err != nil || result.ProviderRef != "namespace/preview" {
		t.Fatalf("preview controller result=%+v err=%v", result, err)
	}
	if seenPath != "/v1/previews" || seenAuthorization != "Bearer 0123456789abcdef0123456789abcdef" || seenOperationID != runID {
		t.Fatalf("controller request headers path=%q auth=%q operation=%q", seenPath, seenAuthorization, seenOperationID)
	}
}

func TestHTTPDeliveryProviderRejectsUnboundedOrUnknownResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"provider":"controller","providerRef":"preview","checks":[],"unexpected":true}`))
	}))
	defer server.Close()
	provider, err := NewHTTPDeliveryProvider(HTTPDeliveryProviderConfig{
		BaseURL: server.URL, BearerToken: "0123456789abcdef0123456789abcdef",
		RequestTimeout: time.Minute, MaxResponseBytes: 1024,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Preview(context.Background(), PreviewProviderRequest{}); err == nil {
		t.Fatal("controller response with unknown authority fields was accepted")
	}
}

func TestHTTPDeliveryProviderPreservesFailedHealthEvidenceWithoutPublicURL(t *testing.T) {
	_, control, _ := deliveryServiceFixture(t)
	expectedRevision := repositoryReference(control.revision.ID, control.revision.PayloadHash)
	expectedReceipt := repositoryReference(control.receipt.ID, control.receipt.PayloadHash)
	var observed struct {
		SchemaVersion    string                     `json:"schemaVersion"`
		Environment      string                     `json:"environment"`
		ExpectedRevision *repository.ExactReference `json:"expectedRevision"`
		ExpectedReceipt  *repository.ExactReference `json:"expectedProductionReceipt"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&observed); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"provider":"controller","providerRef":"deployment/failed","publicUrl":"","checks":[{"id":"health","kind":"health","status":"failed","detail":"503"}]}`))
	}))
	defer server.Close()
	provider, err := NewHTTPDeliveryProvider(HTTPDeliveryProviderConfig{
		BaseURL: server.URL, BearerToken: "0123456789abcdef0123456789abcdef",
		RequestTimeout: time.Minute, MaxResponseBytes: 1024,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.DeployProduction(context.Background(), ProductionProviderRequest{
		RunID: "11111111-1111-4111-8111-111111111111", ProjectID: control.bundle.ProjectID,
		Environment: "production", Bundle: control.bundle, Preview: control.preview, Approval: control.approval,
		Operation: DeploymentPromote, ExpectedHead: &expectedRevision, ExpectedReceipt: &expectedReceipt,
	})
	if err != nil || len(result.Checks) != 1 || result.Checks[0].Status != "failed" || result.PublicURL != "" {
		t.Fatalf("failed health evidence was not preserved: result=%+v err=%v", result, err)
	}
	if observed.SchemaVersion != "release-production-controller-request/v2" || observed.Environment != "production" ||
		observed.ExpectedRevision == nil || *observed.ExpectedRevision != expectedRevision ||
		observed.ExpectedReceipt == nil || *observed.ExpectedReceipt != expectedReceipt {
		t.Fatalf("production controller request lost its exact expected head: %+v", observed)
	}
}
