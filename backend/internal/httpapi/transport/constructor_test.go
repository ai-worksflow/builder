package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

const (
	constructorActorID    = "00000000-0000-4000-8000-000000000001"
	constructorManifestID = "00000000-0000-4000-8000-000000000002"
	constructorContractID = "00000000-0000-4000-8000-000000000003"
	constructorTemplateID = "00000000-0000-4000-8000-000000000004"
	constructorHash       = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type constructorAPIStub struct {
	compileCalls         int
	getForManifestCalls  int
	getCalls             int
	buildManifestID      string
	contractID           string
	actorID              string
	input                constructor.CompileForManifestInput
	compileResult        constructor.ApplicationBuildContract
	getForManifestResult constructor.ApplicationBuildContract
	getResult            constructor.ApplicationBuildContract
	compileError         error
	getForManifestError  error
	getError             error
}

func (s *constructorAPIStub) CompileForManifest(_ context.Context, buildManifestID, actorID string, input constructor.CompileForManifestInput) (constructor.ApplicationBuildContract, error) {
	s.compileCalls++
	s.buildManifestID = buildManifestID
	s.actorID = actorID
	s.input = input
	if s.compileError != nil {
		return constructor.ApplicationBuildContract{}, s.compileError
	}
	if s.compileResult.ID == "" {
		s.compileResult = blockedBuildContract()
	}
	return s.compileResult, nil
}

func (s *constructorAPIStub) GetForManifest(_ context.Context, buildManifestID, actorID string) (constructor.ApplicationBuildContract, error) {
	s.getForManifestCalls++
	s.buildManifestID = buildManifestID
	s.actorID = actorID
	if s.getForManifestError != nil {
		return constructor.ApplicationBuildContract{}, s.getForManifestError
	}
	if s.getForManifestResult.ID == "" {
		s.getForManifestResult = blockedBuildContract()
	}
	return s.getForManifestResult, nil
}

func (s *constructorAPIStub) Get(_ context.Context, contractID, actorID string) (constructor.ApplicationBuildContract, error) {
	s.getCalls++
	s.contractID = contractID
	s.actorID = actorID
	if s.getError != nil {
		return constructor.ApplicationBuildContract{}, s.getError
	}
	if s.getResult.ID == "" {
		s.getResult = blockedBuildContract()
	}
	return s.getResult, nil
}

func blockedBuildContract() constructor.ApplicationBuildContract {
	return constructor.ApplicationBuildContract{
		ID: constructorContractID, ProjectID: "00000000-0000-4000-8000-000000000005",
		BuildManifestID: constructorManifestID, Status: constructor.StatusBlocked,
		Version: 1, ETag: `"application-build-contract:00000000-0000-4000-8000-000000000003:1"`,
		ContentHash: constructorHash, Contract: constructor.ContractContent{Status: constructor.StatusBlocked},
		MustCount: 3, MustReadyCount: 2, BlockingCount: 1,
		CreatedBy: constructorActorID, CreatedAt: time.Unix(1, 0).UTC(),
	}
}

func constructorRouter(t *testing.T, api ConstructorAPI) *gin.Engine {
	t.Helper()
	return constructorRouterWithMutation(t, api, worksmiddleware.CaptureIdempotencyKey(true))
}

func constructorRouterWithMutation(t *testing.T, api ConstructorAPI, mutation ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler, err := NewConstructorHandler(ConstructorDependencies{Service: api, MaxJSONBodyBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	group := router.Group("/v1", func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session: auth.Session{User: auth.User{ID: constructorActorID}}, Transport: "bearer",
		})
		c.Next()
	})
	if err := RegisterConstructorRoutes(group, handler, mutation...); err != nil {
		t.Fatal(err)
	}
	return router
}

func constructorRequest(router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
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

func TestConstructorCreateUsesMutationMiddlewareAndReturnsBlockedContract(t *testing.T) {
	api := &constructorAPIStub{}
	router := constructorRouter(t, api)
	body := `{"fullStackTemplate":{"id":"` + constructorTemplateID + `","contentHash":"` + constructorHash + `"}}`

	missingKey := constructorRequest(router, http.MethodPost, "/v1/build-manifests/"+constructorManifestID+"/build-contracts", body, nil)
	if missingKey.Code != http.StatusBadRequest || api.compileCalls != 0 {
		t.Fatalf("missing key status=%d calls=%d body=%s", missingKey.Code, api.compileCalls, missingKey.Body.String())
	}
	if missingKey.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing key cache control = %q", missingKey.Header().Get("Cache-Control"))
	}

	created := constructorRequest(
		router, http.MethodPost, "/v1/build-manifests/"+constructorManifestID+"/build-contracts", body,
		map[string]string{"Idempotency-Key": "compile-build-contract-1"},
	)
	if created.Code != http.StatusCreated || api.compileCalls != 1 {
		t.Fatalf("create status=%d calls=%d body=%s", created.Code, api.compileCalls, created.Body.String())
	}
	if api.buildManifestID != constructorManifestID || api.actorID != constructorActorID ||
		api.input.FullStackTemplate.ID != constructorTemplateID || api.input.FullStackTemplate.ContentHash != constructorHash {
		t.Fatalf("unexpected compile call: manifest=%q actor=%q input=%#v", api.buildManifestID, api.actorID, api.input)
	}
	if created.Header().Get("Location") != "/v1/application-build-contracts/"+constructorContractID ||
		created.Header().Get("ETag") != blockedBuildContract().ETag || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create headers = %#v", created.Header())
	}
	var response constructor.ApplicationBuildContract
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil || response.Status != constructor.StatusBlocked {
		t.Fatalf("create payload = %#v err=%v", response, err)
	}
}

func TestConstructorCreateAcceptsOnlyExactTemplateSelection(t *testing.T) {
	tests := []string{
		`{"fullStackTemplate":{"id":"` + constructorTemplateID + `","contentHash":"` + constructorHash + `"},"sources":[]}`,
		`{"fullStackTemplate":{"id":"` + constructorTemplateID + `","contentHash":"` + constructorHash + `","branch":"main"}}`,
	}
	for index, body := range tests {
		api := &constructorAPIStub{}
		response := constructorRequest(
			constructorRouter(t, api), http.MethodPost, "/v1/build-manifests/"+constructorManifestID+"/build-contracts", body,
			map[string]string{"Idempotency-Key": "unknown-field-" + string(rune('a'+index))},
		)
		if response.Code != http.StatusBadRequest || api.compileCalls != 0 {
			t.Fatalf("case %d status=%d calls=%d body=%s", index, response.Code, api.compileCalls, response.Body.String())
		}
		var details map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil || details["code"] != "unknown_json_field" {
			t.Fatalf("case %d problem=%#v err=%v", index, details, err)
		}
	}
}

func TestConstructorGetRoutesReturnImmutableValidators(t *testing.T) {
	api := &constructorAPIStub{}
	router := constructorRouter(t, api)

	byManifest := constructorRequest(router, http.MethodGet, "/v1/build-manifests/"+constructorManifestID+"/build-contract", "", nil)
	if byManifest.Code != http.StatusOK || api.getForManifestCalls != 1 || api.buildManifestID != constructorManifestID {
		t.Fatalf("manifest get status=%d calls=%d id=%q body=%s", byManifest.Code, api.getForManifestCalls, api.buildManifestID, byManifest.Body.String())
	}
	byID := constructorRequest(router, http.MethodGet, "/v1/application-build-contracts/"+constructorContractID, "", nil)
	if byID.Code != http.StatusOK || api.getCalls != 1 || api.contractID != constructorContractID {
		t.Fatalf("contract get status=%d calls=%d id=%q body=%s", byID.Code, api.getCalls, api.contractID, byID.Body.String())
	}
	for _, response := range []*httptest.ResponseRecorder{byManifest, byID} {
		if response.Header().Get("ETag") != blockedBuildContract().ETag || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("get headers = %#v", response.Header())
		}
	}
}

func TestConstructorMapsServiceProblems(t *testing.T) {
	compileAPI := &constructorAPIStub{compileError: core.ErrInvalidInput}
	body := `{"fullStackTemplate":{"id":"` + constructorTemplateID + `","contentHash":"` + constructorHash + `"}}`
	invalid := constructorRequest(
		constructorRouter(t, compileAPI), http.MethodPost, "/v1/build-manifests/"+constructorManifestID+"/build-contracts", body,
		map[string]string{"Idempotency-Key": "invalid-build-contract"},
	)
	assertConstructorProblem(t, invalid, http.StatusUnprocessableEntity, "invalid_input")

	getAPI := &constructorAPIStub{getForManifestError: core.ErrNotFound}
	missing := constructorRequest(constructorRouter(t, getAPI), http.MethodGet, "/v1/build-manifests/"+constructorManifestID+"/build-contract", "", nil)
	assertConstructorProblem(t, missing, http.StatusNotFound, "not_found")
	if invalid.Header().Get("Cache-Control") != "no-store" || missing.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("problem responses must be no-store: invalid=%q missing=%q", invalid.Header().Get("Cache-Control"), missing.Header().Get("Cache-Control"))
	}
}

func TestConstructorHandlerRejectsMissingDependencies(t *testing.T) {
	if _, err := NewConstructorHandler(ConstructorDependencies{}); err == nil {
		t.Fatal("expected missing service error")
	}
	if err := RegisterConstructorRoutes(nil, &ConstructorHandler{}); err == nil {
		t.Fatal("expected missing routes error")
	}
	if err := RegisterConstructorRoutes(gin.New(), nil); err == nil {
		t.Fatal("expected missing handler error")
	}
}

func assertConstructorProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var details map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil {
		t.Fatalf("decode problem: %v body=%s", err, response.Body.String())
	}
	if response.Code != status || details["code"] != code {
		t.Fatalf("problem status=%d payload=%#v", response.Code, details)
	}
}
