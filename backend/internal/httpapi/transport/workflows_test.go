package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/domain"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
)

type workflowAuthenticator struct{ userID string }

func (a workflowAuthenticator) Authenticate(context.Context, string) (auth.Session, error) {
	return auth.Session{ID: uuid.NewString(), User: auth.User{ID: a.userID, Email: "owner@example.com", DisplayName: "Owner"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type fakeWorkflowAPI struct {
	actorID    string
	projectID  string
	start      runtime.StartRequest
	approveErr error
	run        *runtime.RunRecord
	runPage    runtime.RunPage
}

func (f *fakeWorkflowAPI) ListDefinitions(context.Context, string, string) ([]runtime.DefinitionRecord, error) {
	return nil, nil
}
func (f *fakeWorkflowAPI) ListDefinitionVersions(context.Context, string, string, string) ([]runtime.DefinitionRecord, error) {
	return nil, nil
}
func (f *fakeWorkflowAPI) CreateDefinition(context.Context, string, string, runtime.CreateDefinitionInput) (runtime.DefinitionRecord, error) {
	return runtime.DefinitionRecord{}, nil
}
func (f *fakeWorkflowAPI) CreateDefinitionVersion(context.Context, string, string, string, runtime.CreateDefinitionVersionInput) (runtime.DefinitionRecord, error) {
	return runtime.DefinitionRecord{}, nil
}
func (f *fakeWorkflowAPI) PublishDefinitionVersion(context.Context, string, string, string, string) (runtime.DefinitionRecord, error) {
	return runtime.DefinitionRecord{}, nil
}
func (f *fakeWorkflowAPI) Start(_ context.Context, projectID, actorID string, request runtime.StartRequest) (*runtime.RunRecord, error) {
	f.projectID = projectID
	f.actorID = actorID
	f.start = request
	if f.run == nil {
		f.run = &runtime.RunRecord{ID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: request.DefinitionVersionID, Status: runtime.RunRunning, EventCursor: 1, Nodes: map[string]*runtime.NodeRecord{}}
	}
	return f.run, nil
}
func (f *fakeWorkflowAPI) GetRun(context.Context, string, string, string) (*runtime.RunRecord, error) {
	return f.run, nil
}
func (f *fakeWorkflowAPI) ListRuns(context.Context, string, string, runtime.RunListOptions) (runtime.RunPage, error) {
	return f.runPage, nil
}
func (f *fakeWorkflowAPI) Events(context.Context, string, string, string, uint64, int) ([]runtime.Event, error) {
	return nil, nil
}
func (f *fakeWorkflowAPI) Resume(context.Context, string, string, string, string, json.RawMessage) error {
	return nil
}
func (f *fakeWorkflowAPI) RecordProposal(context.Context, string, string, string, string, domain.ProposalRef) error {
	return nil
}
func (f *fakeWorkflowAPI) ResolveReview(context.Context, string, string, string, string, runtime.ReviewResolution, string) error {
	return f.approveErr
}
func (f *fakeWorkflowAPI) Cancel(context.Context, string, string, string, string) error { return nil }
func (f *fakeWorkflowAPI) Retry(context.Context, string, string, string, string, string) error {
	return nil
}
func (f *fakeWorkflowAPI) Waive(context.Context, string, string, string, string, string) error {
	return nil
}

func workflowRouterForTest(t *testing.T, api WorkflowAPI, userID string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	security := config.SecurityConfig{Session: config.SessionSecurityConfig{CookieName: "session"}}
	group := router.Group("/v1", worksmiddleware.RequireAuthentication(workflowAuthenticator{userID: userID}, security))
	handler, err := NewWorkflowHandler(WorkflowDependencies{Facade: api, MaxJSONBodyBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterWorkflowRoutes(group, handler); err != nil {
		t.Fatal(err)
	}
	return router
}
func workflowRequest(router http.Handler, method, path string, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestWorkflowStartHandlerUsesAuthenticatedActorAndStrictPins(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	api := &fakeWorkflowAPI{}
	router := workflowRouterForTest(t, api, userID)
	hash, _ := domain.CanonicalHash(map[string]any{"manifest": true})
	versionID, manifestID := uuid.NewString(), uuid.NewString()
	body := `{"definitionVersionId":"` + versionID + `","inputManifest":{"id":"` + manifestID + `","hash":"` + hash + `"},"scope":{"slice":"all"}}`
	response := workflowRequest(router, http.MethodPost, "/v1/projects/"+projectID+"/workflow-runs", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if api.actorID != userID || api.projectID != projectID || api.start.InputManifest.ID != manifestID {
		t.Fatalf("handler lost authenticated scope: %+v", api)
	}
	bad := workflowRequest(router, http.MethodPost, "/v1/projects/"+projectID+"/workflow-runs", `{"definitionVersionId":"x","inputManifest":{"id":"x","hash":"x"},"unknown":true}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d", bad.Code)
	}
}

func TestWorkflowHandlerReturnsETagAndMapsSelfApproval(t *testing.T) {
	projectID, userID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeWorkflowAPI{run: &runtime.RunRecord{ID: runID, ProjectID: projectID, Status: runtime.RunWaitingReview, EventCursor: 9, Nodes: map[string]*runtime.NodeRecord{}}, approveErr: domain.ErrSelfApproval}
	router := workflowRouterForTest(t, api, userID)
	get := workflowRequest(router, http.MethodGet, "/v1/projects/"+projectID+"/workflow-runs/"+runID, "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"workflow-run:`+runID+`:9"` {
		t.Fatalf("GET status=%d etag=%q", get.Code, get.Header().Get("ETag"))
	}
	approve := workflowRequest(router, http.MethodPost, "/v1/projects/"+projectID+"/workflow-runs/"+runID+"/approve", `{"nodeKey":"review","resolution":"approve"}`)
	if approve.Code != http.StatusConflict {
		t.Fatalf("approve status=%d body=%s", approve.Code, approve.Body.String())
	}
}

func TestWorkflowHandlerListsRunsWithStrictQuery(t *testing.T) {
	projectID, userID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeWorkflowAPI{runPage: runtime.RunPage{Items: []runtime.RunSummary{{ID: runID, ProjectID: projectID, Status: runtime.RunRunning}}}}
	router := workflowRouterForTest(t, api, userID)
	response := workflowRequest(router, http.MethodGet, "/v1/projects/"+projectID+"/workflow-runs?status=running&limit=20", "")
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(runID)) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	for _, path := range []string{
		"/v1/projects/" + projectID + "/workflow-runs?limit=101",
		"/v1/projects/" + projectID + "/workflow-runs?unknown=true",
	} {
		invalid := workflowRequest(router, http.MethodGet, path, "")
		if invalid.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid query %q status=%d body=%s", path, invalid.Code, invalid.Body.String())
		}
	}
}
