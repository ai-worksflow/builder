package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/delivery"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

type deliveryAuthenticator struct{ userID string }

func (a deliveryAuthenticator) Authenticate(context.Context, string) (auth.Session, error) {
	return auth.Session{
		ID: uuid.NewString(), User: auth.User{ID: a.userID, Email: "owner@example.com", DisplayName: "Owner"},
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

type fakeDeliveryAPI struct {
	qualityActor   string
	qualityProject string
	qualityInput   delivery.QualityRunInput
	qualityErr     error
	report         delivery.QualityReport

	exportActor   string
	exportProject string
	exportInput   delivery.ExportInput
	exportErr     error
	archive       delivery.Archive

	publishActor   string
	publishProject string
	publishETag    string
	publishInput   delivery.PublishInput
	publishErr     error
	deployment     delivery.Deployment

	rollbackActor string
	rollbackETag  string
	rollbackInput delivery.RollbackInput
	rollbackCalls int

	qualityListRevision string
	logs                []delivery.DeploymentLog
}

func (f *fakeDeliveryAPI) Evaluate(_ context.Context, projectID, actorID string, input delivery.QualityRunInput) (delivery.QualityReport, error) {
	f.qualityActor, f.qualityProject, f.qualityInput = actorID, projectID, input
	return f.report, f.qualityErr
}

func (f *fakeDeliveryAPI) Get(_ context.Context, _ string, actorID string) (delivery.QualityReport, error) {
	f.qualityActor = actorID
	return f.report, f.qualityErr
}

func (f *fakeDeliveryAPI) List(_ context.Context, _ string, actorID, revisionID string) ([]delivery.QualityReport, error) {
	f.qualityActor, f.qualityListRevision = actorID, revisionID
	return []delivery.QualityReport{f.report}, f.qualityErr
}

func (f *fakeDeliveryAPI) Export(_ context.Context, projectID, actorID string, input delivery.ExportInput) (delivery.Archive, error) {
	f.exportActor, f.exportProject, f.exportInput = actorID, projectID, input
	return f.archive, f.exportErr
}

func (f *fakeDeliveryAPI) Publish(_ context.Context, projectID, actorID, expectedETag string, input delivery.PublishInput) (delivery.Deployment, error) {
	f.publishActor, f.publishProject, f.publishETag, f.publishInput = actorID, projectID, expectedETag, input
	return f.deployment, f.publishErr
}

func (f *fakeDeliveryAPI) Rollback(_ context.Context, _ string, actorID, expectedETag string, input delivery.RollbackInput) (delivery.Deployment, error) {
	f.rollbackCalls++
	f.rollbackActor, f.rollbackETag, f.rollbackInput = actorID, expectedETag, input
	return f.deployment, f.publishErr
}

func (f *fakeDeliveryAPI) GetDeployment(_ context.Context, _ string, actorID string) (delivery.Deployment, error) {
	f.publishActor = actorID
	return f.deployment, f.publishErr
}

// Go cannot overload Get, so publish methods are exposed through an adapter.
type fakeDeliveryPublishAPI struct{ source *fakeDeliveryAPI }

func (f fakeDeliveryPublishAPI) Publish(ctx context.Context, projectID, actorID, etag string, input delivery.PublishInput) (delivery.Deployment, error) {
	return f.source.Publish(ctx, projectID, actorID, etag, input)
}
func (f fakeDeliveryPublishAPI) Rollback(ctx context.Context, deploymentID, actorID, etag string, input delivery.RollbackInput) (delivery.Deployment, error) {
	return f.source.Rollback(ctx, deploymentID, actorID, etag, input)
}
func (f fakeDeliveryPublishAPI) Get(_ context.Context, _ string, actorID string) (delivery.Deployment, error) {
	f.source.publishActor = actorID
	return f.source.deployment, f.source.publishErr
}
func (f fakeDeliveryPublishAPI) List(_ context.Context, projectID, actorID string) ([]delivery.Deployment, error) {
	f.source.publishProject, f.source.publishActor = projectID, actorID
	return []delivery.Deployment{f.source.deployment}, f.source.publishErr
}
func (f fakeDeliveryPublishAPI) Logs(_ context.Context, _ string, actorID string) ([]delivery.DeploymentLog, error) {
	f.source.publishActor = actorID
	return f.source.logs, f.source.publishErr
}

type fakeStaticAssets struct {
	deploymentID string
	versionID    string
	asset        string
}

func (s *fakeStaticAssets) ServeAsset(response http.ResponseWriter, _ *http.Request, deploymentID, versionID, asset string) {
	s.deploymentID, s.versionID, s.asset = deploymentID, versionID, asset
	response.Header().Set("Content-Type", "text/html")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("published"))
}

func deliveryRouterForTest(t *testing.T, api *fakeDeliveryAPI, userID string, static delivery.StaticAssetServer, mutation ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	security := config.SecurityConfig{Session: config.SessionSecurityConfig{CookieName: "session"}}
	protected := router.Group("/v1", worksmiddleware.RequireAuthentication(deliveryAuthenticator{userID: userID}, security))
	handler, err := NewDeliveryHandler(DeliveryDependencies{
		Quality: api, Export: api, Publish: fakeDeliveryPublishAPI{source: api}, StaticAssets: static,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterDeliveryRoutes(protected, handler, mutation...); err != nil {
		t.Fatal(err)
	}
	if static != nil {
		if err := RegisterDeliveryPublicRoutes(router, handler); err != nil {
			t.Fatal(err)
		}
	}
	return router
}

func deliveryHTTP(router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func deliveryVersionRefJSON() (core.VersionRef, string) {
	reference := core.VersionRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(),
		ContentHash: "sha256:" + strings.Repeat("a", 64),
	}
	payload, _ := json.Marshal(reference)
	return reference, string(payload)
}

func TestDeliveryQualityRoutesUseAuthenticatedActorStrictDTOAndETag(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	reference, encodedRef := deliveryVersionRefJSON()
	report := delivery.QualityReport{ID: uuid.NewString(), ProjectID: projectID, WorkspaceRevision: reference, ETag: `"quality-run:test:1"`}
	api := &fakeDeliveryAPI{report: report}
	mutationCalls := 0
	router := deliveryRouterForTest(t, api, userID, nil, func(c *gin.Context) {
		mutationCalls++
		c.Next()
	})

	created := deliveryHTTP(router, http.MethodPost, "/v1/projects/"+projectID+"/quality-runs", `{"workspaceRevision":`+encodedRef+`}`, nil)
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != report.ETag || created.Header().Get("Location") == "" {
		t.Fatalf("create status=%d headers=%v body=%s", created.Code, created.Header(), created.Body.String())
	}
	if mutationCalls != 1 || api.qualityActor != userID || api.qualityProject != projectID || api.qualityInput.WorkflowRunID != nil || api.qualityInput.WorkspaceRevision.RevisionID != reference.RevisionID {
		t.Fatalf("quality handler lost trusted scope: api=%+v middleware=%d", api, mutationCalls)
	}
	spoofed := deliveryHTTP(router, http.MethodPost, "/v1/projects/"+projectID+"/quality-runs", `{"workspaceRevision":`+encodedRef+`,"workflowRunId":"`+uuid.NewString()+`"}`, nil)
	if spoofed.Code != http.StatusBadRequest || !strings.Contains(spoofed.Body.String(), "unknown_json_field") {
		t.Fatalf("workflow association spoof status=%d body=%s", spoofed.Code, spoofed.Body.String())
	}
	conditional := deliveryHTTP(router, http.MethodGet, "/v1/quality-runs/"+report.ID, "", map[string]string{"If-None-Match": report.ETag})
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("quality conditional GET status=%d body=%s", conditional.Code, conditional.Body.String())
	}
	filtered := deliveryHTTP(router, http.MethodGet, "/v1/projects/"+projectID+"/quality-runs?workspaceRevisionId="+reference.RevisionID, "", nil)
	if filtered.Code != http.StatusOK || api.qualityListRevision != reference.RevisionID {
		t.Fatalf("quality list status=%d filter=%q", filtered.Code, api.qualityListRevision)
	}
	badQuery := deliveryHTTP(router, http.MethodGet, "/v1/projects/"+projectID+"/quality-runs?secret=true", "", nil)
	if badQuery.Code != http.StatusBadRequest {
		t.Fatalf("unknown query status=%d", badQuery.Code)
	}
}

func TestDeliveryExportDefaultsToRedactionAndEmitsExactArchiveHeaders(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	reference, encodedRef := deliveryVersionRefJSON()
	api := &fakeDeliveryAPI{archive: delivery.Archive{
		Filename: "project-source.zip", ContentType: "application/zip", Data: []byte("exact-zip"),
		FileCount: 3, Checksum: "sha256:" + strings.Repeat("b", 64), Redactions: []string{".env"},
	}}
	mutationCalls := 0
	router := deliveryRouterForTest(t, api, userID, nil, func(c *gin.Context) {
		mutationCalls++
		c.Next()
	})
	response := deliveryHTTP(router, http.MethodPost, "/v1/projects/"+projectID+"/exports", `{"kind":"source","revision":`+encodedRef+`}`, nil)
	if response.Code != http.StatusOK || response.Body.String() != "exact-zip" || response.Header().Get("Digest") == "" || response.Header().Get("X-Archive-File-Count") != "3" {
		t.Fatalf("export status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	if api.exportActor != userID || api.exportProject != projectID || !api.exportInput.RedactSensitive || api.exportInput.Revision == nil || api.exportInput.Revision.RevisionID != reference.RevisionID {
		t.Fatalf("export was not safely scoped: %+v", api.exportInput)
	}
	if mutationCalls != 0 {
		t.Fatalf("read-only export traversed mutation/idempotency middleware %d times", mutationCalls)
	}
}

func TestDeliveryPublishAndRollbackEnforceStrongConditionalWrites(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	reference, encodedRef := deliveryVersionRefJSON()
	deploymentID := uuid.NewString()
	api := &fakeDeliveryAPI{deployment: delivery.Deployment{
		ID: deploymentID, ProjectID: projectID, Environment: delivery.EnvironmentPreview,
		PublicURL: "/published/ready/", ETag: `"deployment:` + deploymentID + `:2"`,
	}}
	router := deliveryRouterForTest(t, api, userID, nil)
	body := `{"environment":"preview","workspaceRevision":` + encodedRef + `}`
	created := deliveryHTTP(router, http.MethodPost, "/v1/projects/"+projectID+"/deployments", body, nil)
	if created.Code != http.StatusCreated || api.publishETag != "" || api.publishActor != userID || api.publishInput.WorkspaceRevision == nil || api.publishInput.WorkspaceRevision.RevisionID != reference.RevisionID {
		t.Fatalf("initial publish status=%d api=%+v body=%s", created.Code, api, created.Body.String())
	}
	currentETag := api.deployment.ETag
	updated := deliveryHTTP(router, http.MethodPost, "/v1/projects/"+projectID+"/deployments", body, map[string]string{"If-Match": currentETag})
	if updated.Code != http.StatusCreated || api.publishETag != currentETag {
		t.Fatalf("conditional publish status=%d etag=%q", updated.Code, api.publishETag)
	}
	weak := deliveryHTTP(router, http.MethodPost, "/v1/projects/"+projectID+"/deployments", body, map[string]string{"If-Match": "W/" + currentETag})
	if weak.Code != http.StatusBadRequest {
		t.Fatalf("weak If-Match status=%d body=%s", weak.Code, weak.Body.String())
	}
	rollbackBody := `{"targetVersionId":"` + uuid.NewString() + `"}`
	missing := deliveryHTTP(router, http.MethodPost, "/v1/deployments/"+deploymentID+"/rollback", rollbackBody, nil)
	if missing.Code != http.StatusPreconditionRequired || api.rollbackCalls != 0 {
		t.Fatalf("missing rollback precondition status=%d calls=%d", missing.Code, api.rollbackCalls)
	}
	rolledBack := deliveryHTTP(router, http.MethodPost, "/v1/deployments/"+deploymentID+"/rollback", rollbackBody, map[string]string{"If-Match": currentETag})
	if rolledBack.Code != http.StatusOK || api.rollbackCalls != 1 || api.rollbackActor != userID || api.rollbackETag != currentETag {
		t.Fatalf("rollback status=%d api=%+v", rolledBack.Code, api)
	}
}

func TestDeliveryReadRoutesAndPublicAssetsAreCovered(t *testing.T) {
	projectID, userID, deploymentID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeDeliveryAPI{
		deployment: delivery.Deployment{ID: deploymentID, ProjectID: projectID, ETag: `"deployment:test:1"`},
		logs:       []delivery.DeploymentLog{{ID: uuid.NewString(), DeploymentID: deploymentID, Sequence: 1, Message: "ready"}},
	}
	static := &fakeStaticAssets{}
	router := deliveryRouterForTest(t, api, userID, static)
	for _, path := range []string{
		"/v1/projects/" + projectID + "/deployments",
		"/v1/deployments/" + deploymentID,
		"/v1/deployments/" + deploymentID + "/logs",
	} {
		response := deliveryHTTP(router, http.MethodGet, path, "", nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s status=%d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		}
	}
	asset := deliveryHTTP(router, http.MethodGet, "/published/"+deploymentID+"/"+versionID+"/assets/app.js", "", nil)
	if asset.Code != http.StatusOK || asset.Body.String() != "published" || static.deploymentID != deploymentID || static.versionID != versionID || static.asset != "assets/app.js" {
		t.Fatalf("public asset route mismatch: status=%d static=%+v", asset.Code, static)
	}
}

func TestDeliveryErrorsAreRFC9457AndHideInternalCause(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	_, encodedRef := deliveryVersionRefJSON()
	api := &fakeDeliveryAPI{qualityErr: &delivery.DeliveryError{
		Code: delivery.CodeInternal, Status: http.StatusInternalServerError,
		Detail: "query users with password secret", Cause: errors.New("postgres password=secret"),
	}}
	router := deliveryRouterForTest(t, api, userID, nil)
	response := deliveryHTTP(router, http.MethodPost, "/v1/projects/"+projectID+"/quality-runs", `{"workspaceRevision":`+encodedRef+`}`, nil)
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if strings.Contains(response.Body.String(), "password") || !strings.Contains(response.Body.String(), `"type":"urn:worksflow:problem:internal_error"`) {
		t.Fatalf("internal cause leaked or RFC9457 missing: %s", response.Body.String())
	}
}
