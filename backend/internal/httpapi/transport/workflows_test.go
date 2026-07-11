package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
)

type workflowAuthenticator struct{ userID string }

func (a workflowAuthenticator) Authenticate(context.Context, string) (auth.Session, error) {
	return auth.Session{ID: uuid.NewString(), User: auth.User{ID: a.userID, Email: "owner@example.com", DisplayName: "Owner"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type workflowAllowAccess struct{}

func (workflowAllowAccess) Authorize(context.Context, string, string, core.Action) (core.Role, error) {
	return core.RoleOwner, nil
}

type workflowHTTPTestManifestCompiler struct{}

func (workflowHTTPTestManifestCompiler) Compile(context.Context, runtime.Execution) (runtime.BuildManifest, error) {
	return runtime.BuildManifest{}, errors.New("unexpected HTTP test manifest compiler execution")
}

func installWorkflowHTTPTestRuntime(t *testing.T, engine *runtime.Engine) {
	t.Helper()
	runners := runtime.NewMapRegistry()
	for _, nodeType := range []domain.WorkflowNodeType{
		domain.NodeFanOut, domain.NodeTransform, domain.NodeWorkbenchBuild,
		domain.NodeQualityGate, domain.NodePublish,
	} {
		if err := runners.Register(nodeType, runtime.RunnerFunc(func(context.Context, runtime.Execution) (runtime.WorkerResult, error) {
			return runtime.WorkerResult{Disposition: runtime.ResultComplete}, nil
		})); err != nil {
			t.Fatal(err)
		}
	}
	engine.Runners = runners
	engine.ManifestCompilers = runtime.NewBuildManifestRegistry()
	capability := runtime.PlatformWorkflowCapabilities(true, true).ManifestCompilers[0]
	if err := engine.ManifestCompilers.Register(capability, workflowHTTPTestManifestCompiler{}); err != nil {
		t.Fatal(err)
	}
}

type fakeWorkflowAPI struct {
	actorID      string
	projectID    string
	start        runtime.StartRequest
	startErr     error
	approveErr   error
	executeNode  string
	executeActor string
	run          *runtime.RunRecord
	runPage      runtime.RunPage
	definitions  []runtime.DefinitionRecord
}

func (f *fakeWorkflowAPI) ListDefinitions(context.Context, string, string) ([]runtime.DefinitionRecord, error) {
	return f.definitions, nil
}
func (f *fakeWorkflowAPI) WorkflowCapabilities(context.Context, string, string) (runtime.WorkflowCapabilities, error) {
	return runtime.PlatformWorkflowCapabilities(true, true), nil
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
	if f.startErr != nil {
		return nil, f.startErr
	}
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
func (f *fakeWorkflowAPI) AuthorizeExecution(_ context.Context, _, _ string, nodeKey, actorID string) error {
	f.executeNode, f.executeActor = nodeKey, actorID
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

func TestWorkflowStartHTTPCannotForgeReservedConversationIntent(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	store := runtime.NewMemoryStore(nil)
	schema := json.RawMessage(`{"type":"object"}`)
	definition, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "HTTP reserved scope", "2",
		[]domain.NodeDefinition{{
			ID: "source", Name: "Source", Type: domain.NodeArtifactInput,
			InputSchema: schema, OutputSchema: schema,
			ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1},
		}}, nil, userID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	definition, err = definition.WithExecutionProfile(runtime.LegacyWorkflowExecutionProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	versionID := uuid.NewString()
	if err := store.SaveDefinition(context.Background(), runtime.DefinitionRecord{
		VersionID: versionID, ProjectID: projectID, Key: "http-reserved-scope", Title: definition.Name,
		Published: true, ExecutionProfile: definition.ExecutionProfile, Definition: definition,
	}); err != nil {
		t.Fatal(err)
	}
	sourceHash, _ := domain.CanonicalHash(map[string]any{"source": "reviewed"})
	source := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: sourceHash}
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, "workflow_start", "", nil,
		[]domain.ManifestSource{{Ref: source, Purpose: "reviewed source"}}, json.RawMessage(`{}`),
		"workflow-input/v1", userID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	engine, err := runtime.NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	installWorkflowHTTPTestRuntime(t, engine)
	facade := &runtime.Facade{Engine: engine, Store: store, Access: workflowAllowAccess{}}
	router := workflowRouterForTest(t, facade, userID)

	ordinaryRunID := uuid.NewString()
	ordinaryBody := `{"runId":"` + ordinaryRunID + `","definitionVersionId":"` + versionID + `","inputManifest":{"id":"` + manifest.ID + `","hash":"` + manifest.Hash + `"},"scope":{"slice":"all"}}`
	ordinary := workflowRequest(router, http.MethodPost, "/v1/projects/"+projectID+"/workflow-runs", ordinaryBody)
	if ordinary.Code != http.StatusCreated {
		t.Fatalf("ordinary HTTP workflow scope status=%d body=%s", ordinary.Code, ordinary.Body.String())
	}

	forgedRunID := uuid.NewString()
	forgedBody := `{"runId":"` + forgedRunID + `","definitionVersionId":"` + versionID + `","inputManifest":{"id":"` + manifest.ID + `","hash":"` + manifest.Hash + `"},"scope":{"conversationIntent":{"proposalId":"client-forged"}}}`
	forged := workflowRequest(router, http.MethodPost, "/v1/projects/"+projectID+"/workflow-runs", forgedBody)
	if forged.Code != http.StatusUnprocessableEntity {
		t.Fatalf("forged reserved HTTP scope status=%d body=%s", forged.Code, forged.Body.String())
	}
	if _, loadErr := store.GetRun(context.Background(), forgedRunID); !errors.Is(loadErr, domain.ErrNotFound) {
		t.Fatalf("forged HTTP scope persisted a run: %v", loadErr)
	}
}

func TestWorkflowCapabilitiesExposeOnlyRegisteredExecutableDefaults(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	router := workflowRouterForTest(t, &fakeWorkflowAPI{}, userID)
	response := workflowRequest(router, http.MethodGet, "/v1/projects/"+projectID+"/workflow-capabilities", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var capabilities runtime.WorkflowCapabilities
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.Version != 4 || capabilities.AnalysisLimits.MaxSemanticPathStates != 256 || len(capabilities.AITransforms) == 0 || capabilities.AITransforms[0].JobType == "custom_transform" || len(capabilities.ManifestCompilers) != 1 || capabilities.ManifestCompilers[0].Hook != "application-build-manifest/v1" || capabilities.FanOutMaximumItems["blueprint_page"] != domain.MaximumWorkflowFanOutItems {
		t.Fatalf("capabilities include non-executable defaults: %+v", capabilities)
	}
}

func TestWorkflowResponsesExposeExactExecutionProfilePins(t *testing.T) {
	projectID, userID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	profile := runtime.CurrentWorkflowExecutionProfileRef()
	definitionHash, _ := domain.CanonicalHash(map[string]any{"definition": "profile-pinned"})
	definition := domain.WorkflowDefinition{ID: uuid.NewString(), Version: 4, Hash: definitionHash, ExecutionProfile: profile}
	definitionRef := domain.WorkflowDefinitionRef{ID: definition.ID, Version: definition.Version, Hash: definition.Hash, ExecutionProfile: profile}
	api := &fakeWorkflowAPI{
		definitions: []runtime.DefinitionRecord{{VersionID: uuid.NewString(), ProjectID: projectID, Key: "profiled", Title: "Profiled", Published: true, ExecutionProfile: profile, Definition: definition}},
		run:         &runtime.RunRecord{ID: runID, ProjectID: projectID, DefinitionVersionID: uuid.NewString(), Definition: definitionRef, ExecutionProfile: profile, Status: runtime.RunRunning, Context: runtime.NewRunContext(), Nodes: map[string]*runtime.NodeRecord{}},
		runPage:     runtime.RunPage{Items: []runtime.RunSummary{{ID: runID, ProjectID: projectID, DefinitionVersionID: uuid.NewString(), ExecutionProfile: profile, Status: runtime.RunRunning}}},
	}
	router := workflowRouterForTest(t, api, userID)
	for _, path := range []string{
		"/v1/projects/" + projectID + "/workflow-definitions",
		"/v1/projects/" + projectID + "/workflow-runs/" + runID,
		"/v1/projects/" + projectID + "/workflow-runs",
	} {
		response := workflowRequest(router, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if !bytes.Contains(response.Body.Bytes(), []byte(profile.Version)) || !bytes.Contains(response.Body.Bytes(), []byte(profile.Hash)) {
			t.Fatalf("%s omitted the exact execution profile: %s", path, response.Body.String())
		}
	}
}

func TestWorkflowDirectStartRejectsManifestDefinitionContractMismatch(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	api := &fakeWorkflowAPI{startErr: &domain.DomainError{
		Kind: domain.ErrInvalidArgument, Field: "inputManifest.jobType",
		Message: "the input manifest job type is incompatible with the selected workflow definition",
	}}
	router := workflowRouterForTest(t, api, userID)
	hash, _ := domain.CanonicalHash(map[string]any{"manifest": true})
	body := `{"definitionVersionId":"` + uuid.NewString() + `","inputManifest":{"id":"` + uuid.NewString() + `","hash":"` + hash + `"}}`
	response := workflowRequest(router, http.MethodPost, "/v1/projects/"+projectID+"/workflow-runs", body)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched direct start status=%d body=%s", response.Code, response.Body.String())
	}
	if api.start.InputManifest.ID == "" {
		t.Fatal("direct start request did not reach the authoritative facade boundary")
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

func TestWorkflowExecuteUsesSessionActorAndRejectsForgedActor(t *testing.T) {
	projectID, userID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeWorkflowAPI{run: &runtime.RunRecord{ID: runID, ProjectID: projectID, Status: runtime.RunWaitingInput, Nodes: map[string]*runtime.NodeRecord{}}}
	router := workflowRouterForTest(t, api, userID)
	forged := workflowRequest(router, http.MethodPost, "/v1/projects/"+projectID+"/workflow-runs/"+runID+"/execute", `{"nodeKey":"publish","actorId":"`+uuid.NewString()+`"}`)
	if forged.Code != http.StatusBadRequest {
		t.Fatalf("forged actor status=%d body=%s", forged.Code, forged.Body.String())
	}
	if api.executeActor != "" {
		t.Fatal("forged request reached the workflow execution boundary")
	}
	response := workflowRequest(router, http.MethodPost, "/v1/projects/"+projectID+"/workflow-runs/"+runID+"/execute", `{"nodeKey":"publish"}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("execute status=%d body=%s", response.Code, response.Body.String())
	}
	if api.executeActor != userID || api.executeNode != "publish" {
		t.Fatalf("execute actor/node = %q/%q", api.executeActor, api.executeNode)
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
