package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/dataruntime"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

type dataAuthenticator struct{ userID string }

func (a dataAuthenticator) Authenticate(context.Context, string) (auth.Session, error) {
	return auth.Session{
		ID: uuid.NewString(), User: auth.User{ID: a.userID, Email: "owner@example.com", DisplayName: "Owner"},
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

type fakeDataAPI struct {
	DataRuntimeAPI
	actorID      string
	projectID    string
	createInput  dataruntime.TableInput
	createErr    error
	connectInput dataruntime.SupabaseConnectionInput
}

func (f *fakeDataAPI) CreateTable(_ context.Context, projectID, actorID string, input dataruntime.TableInput) (dataruntime.Table, error) {
	f.projectID, f.actorID, f.createInput = projectID, actorID, input
	if f.createErr != nil {
		return dataruntime.Table{}, f.createErr
	}
	return dataruntime.Table{ID: uuid.NewString(), Name: input.Name, Columns: []dataruntime.Column{}}, nil
}

func (f *fakeDataAPI) ListRecords(_ context.Context, projectID, tableID, actorID string, limit, offset int) (dataruntime.RecordPage, error) {
	f.projectID, f.actorID = projectID, actorID
	return dataruntime.RecordPage{Records: []dataruntime.Record{}, Total: 0, Limit: limit, Offset: offset}, nil
}

func (f *fakeDataAPI) ConnectSupabase(_ context.Context, projectID, actorID string, input dataruntime.SupabaseConnectionInput) (dataruntime.SupabaseConnectionResult, error) {
	f.projectID, f.actorID, f.connectInput = projectID, actorID, input
	return dataruntime.SupabaseConnectionResult{
		OK: true, Endpoint: "https://demo.supabase.co", Status: 200,
		Message: "Supabase REST connection succeeded.",
	}, nil
}

func dataRouterForTest(t *testing.T, api DataRuntimeAPI, userID string) *gin.Engine {
	t.Helper()
	router := gin.New()
	security := config.SecurityConfig{Session: config.SessionSecurityConfig{CookieName: "session"}}
	group := router.Group("/v1", worksmiddleware.RequireAuthentication(dataAuthenticator{userID: userID}, security))
	handler, err := NewDataHandler(DataDependencies{Service: api, MaxJSONBodyBytes: dataruntime.MaxRequestBytes})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterDataRoutes(group, handler); err != nil {
		t.Fatal(err)
	}
	return router
}

func dataRequest(router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-token")
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

func TestDataCreateTableUsesAuthenticatedActorAndStrictDTO(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	api := &fakeDataAPI{}
	router := dataRouterForTest(t, api, userID)

	response := dataRequest(router, http.MethodPost, "/v1/data/projects/"+projectID+"/tables", `{"name":"users","columns":[]}`, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if api.actorID != userID || api.projectID != projectID || api.createInput.Name != "users" {
		t.Fatalf("handler lost authenticated scope: %+v", api)
	}
	if response.Header().Get("Location") == "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing response headers: %v", response.Header())
	}

	unknown := dataRequest(router, http.MethodPost, "/v1/data/projects/"+projectID+"/tables", `{"name":"users","sql":"DROP TABLE"}`, nil)
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "unknown_json_field") {
		t.Fatalf("unknown field status=%d body=%s", unknown.Code, unknown.Body.String())
	}
}

func TestDataErrorsUseRFC9457AndPaginationIsStrict(t *testing.T) {
	projectID, userID, tableID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeDataAPI{createErr: dataruntime.Conflict("Table users already exists")}
	router := dataRouterForTest(t, api, userID)

	conflict := dataRequest(router, http.MethodPost, "/v1/data/projects/"+projectID+"/tables", `{"name":"users"}`, nil)
	if conflict.Code != http.StatusConflict || conflict.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d content-type=%q body=%s", conflict.Code, conflict.Header().Get("Content-Type"), conflict.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(conflict.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != "urn:worksflow:problem:conflict" || body["status"] != float64(http.StatusConflict) || body["detail"] == "" {
		t.Fatalf("not an RFC9457 problem: %v", body)
	}
	legacy, ok := body["error"].(map[string]any)
	if !ok || legacy["code"] != "conflict" || legacy["message"] == "" {
		t.Fatalf("frontend compatibility extension is missing: %v", body)
	}

	badPage := dataRequest(router, http.MethodGet, "/v1/data/projects/"+projectID+"/tables/"+tableID+"/records?limit=101", "", nil)
	if badPage.Code != http.StatusBadRequest {
		t.Fatalf("bad page status=%d body=%s", badPage.Code, badPage.Body.String())
	}
	unknownQuery := dataRequest(router, http.MethodGet, "/v1/data/projects/"+projectID+"/tables/"+tableID+"/records?sort=desc", "", nil)
	if unknownQuery.Code != http.StatusBadRequest {
		t.Fatalf("unknown query status=%d", unknownQuery.Code)
	}
}

func TestDataSupabaseConnectionRequiresProjectHeaderAndNeverEchoesKey(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	api := &fakeDataAPI{}
	router := dataRouterForTest(t, api, userID)
	body := `{"endpoint":"https://demo.supabase.co","key":"secret-service-key"}`

	missing := dataRequest(router, http.MethodPost, "/v1/data/connect/supabase", body, nil)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing header status=%d body=%s", missing.Code, missing.Body.String())
	}
	response := dataRequest(router, http.MethodPost, "/v1/data/connect/supabase", body, map[string]string{"X-Worksflow-Project-Id": projectID})
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "secret-service-key") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if api.projectID != projectID || api.actorID != userID || api.connectInput.Key != "secret-service-key" {
		t.Fatalf("connection scope/input mismatch: %+v", api)
	}
}

func TestRegisterDataRoutesAppliesMutationMiddlewareOnlyToWrites(t *testing.T) {
	projectID, userID, tableID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeDataAPI{}
	router := gin.New()
	security := config.SecurityConfig{Session: config.SessionSecurityConfig{CookieName: "session"}}
	group := router.Group("/v1", worksmiddleware.RequireAuthentication(dataAuthenticator{userID: userID}, security))
	handler, err := NewDataHandler(DataDependencies{Service: api})
	if err != nil {
		t.Fatal(err)
	}
	mutations := 0
	mutationMiddleware := func(c *gin.Context) {
		mutations++
		c.Next()
	}
	if err := RegisterDataRoutes(group, handler, mutationMiddleware); err != nil {
		t.Fatal(err)
	}
	read := dataRequest(router, http.MethodGet, "/v1/data/projects/"+projectID+"/tables/"+tableID+"/records", "", nil)
	if read.Code != http.StatusOK || mutations != 0 {
		t.Fatalf("read status=%d mutation middleware calls=%d", read.Code, mutations)
	}
	write := dataRequest(router, http.MethodPost, "/v1/data/projects/"+projectID+"/tables", `{"name":"users"}`, nil)
	if write.Code != http.StatusCreated || mutations != 1 {
		t.Fatalf("write status=%d mutation middleware calls=%d", write.Code, mutations)
	}
}
