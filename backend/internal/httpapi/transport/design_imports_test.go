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
	"github.com/worksflow/builder/backend/internal/designimport"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

type designImportAPIStub struct {
	createCalls   int
	decisionCalls int
	commandKey    string
	expectedETag  string
	createError   error
	createErrors  []error
}

func (s *designImportAPIStub) Capabilities(context.Context, string, string) (designimport.Capabilities, error) {
	return designimport.SupportedCapabilities(), nil
}

func (s *designImportAPIStub) Create(_ context.Context, projectID, actorID, commandKey string, _ designimport.CreateInput) (designimport.Import, error) {
	s.createCalls++
	s.commandKey = commandKey
	if len(s.createErrors) > 0 {
		err := s.createErrors[0]
		s.createErrors = s.createErrors[1:]
		if err != nil {
			return designimport.Import{}, err
		}
	}
	if s.createError != nil {
		return designimport.Import{}, s.createError
	}
	return designImportDTO(projectID, actorID), nil
}

func (s *designImportAPIStub) Get(context.Context, string, string) (designimport.Import, error) {
	return designImportDTO("project-1", "actor-1"), nil
}

func (s *designImportAPIStub) List(context.Context, string, string, string) ([]designimport.Import, error) {
	return []designimport.Import{designImportDTO("project-1", "actor-1")}, nil
}

func (s *designImportAPIStub) Decide(_ context.Context, _, actorID, expectedETag string, _ designimport.DecisionInput) (designimport.Import, error) {
	s.decisionCalls++
	s.expectedETag = expectedETag
	return designImportDTO("project-1", actorID), nil
}

func designImportDTO(projectID, actorID string) designimport.Import {
	return designimport.Import{
		ID: "00000000-0000-4000-8000-000000000100", ProjectID: projectID,
		Status: "open", Version: 4, ETag: `"design-import:00000000-0000-4000-8000-000000000100:4"`,
		CreatedBy: actorID, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}
}

func designImportRouter(t *testing.T, api DesignImportAPI) *gin.Engine {
	return designImportRouterWithMutation(t, api, worksmiddleware.CaptureIdempotencyKey(true))
}

func designImportRouterWithMutation(t *testing.T, api DesignImportAPI, mutation ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler, err := NewDesignImportHandler(DesignImportDependencies{Service: api, MaxJSONBodyBytes: designimport.MaxRequestBytes})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	group := router.Group("/v1", func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session:   auth.Session{User: auth.User{ID: "00000000-0000-4000-8000-000000000001"}},
			Transport: "bearer",
		})
		c.Next()
	})
	if err := RegisterDesignImportRoutes(group, handler, mutation...); err != nil {
		t.Fatal(err)
	}
	return router
}

func designImportRequest(router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
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

func TestDesignImportCreateRequiresIdempotencyAndForwardsCommandKey(t *testing.T) {
	api := &designImportAPIStub{}
	router := designImportRouter(t, api)
	body := `{
  "sourceKind":"upload","mode":"upload",
  "pageSpecRevision":{"artifactId":"a","revisionId":"r","contentHash":"sha256:h"},
  "file":{"name":"design.json","mediaType":"application/json","contentBase64":"e30="}
}`
	missing := designImportRequest(router, http.MethodPost, "/v1/projects/project-1/design-imports", body, nil)
	if missing.Code != http.StatusBadRequest || api.createCalls != 0 {
		t.Fatalf("missing key status=%d calls=%d body=%s", missing.Code, api.createCalls, missing.Body.String())
	}
	created := designImportRequest(router, http.MethodPost, "/v1/projects/project-1/design-imports", body, map[string]string{"Idempotency-Key": "design-import-create-1"})
	if created.Code != http.StatusCreated || api.createCalls != 1 || api.commandKey != "design-import-create-1" {
		t.Fatalf("create status=%d calls=%d key=%q body=%s", created.Code, api.createCalls, api.commandKey, created.Body.String())
	}
	if created.Header().Get("ETag") == "" || created.Header().Get("Location") != "/v1/design-imports/00000000-0000-4000-8000-000000000100" {
		t.Fatalf("missing create validators: %#v", created.Header())
	}
}

func TestDesignImportDecisionRequiresStrongIfMatch(t *testing.T) {
	api := &designImportAPIStub{}
	router := designImportRouter(t, api)
	body := `{"decision":"approve","version":4}`
	headers := map[string]string{"Idempotency-Key": "design-import-approve-1"}
	missing := designImportRequest(router, http.MethodPost, "/v1/design-imports/import-1/decision", body, headers)
	if missing.Code != http.StatusPreconditionRequired || api.decisionCalls != 0 {
		t.Fatalf("missing If-Match status=%d calls=%d body=%s", missing.Code, api.decisionCalls, missing.Body.String())
	}
	headers["If-Match"] = `"design-import:import-1:4"`
	decided := designImportRequest(router, http.MethodPost, "/v1/design-imports/import-1/decision", body, headers)
	if decided.Code != http.StatusOK || api.decisionCalls != 1 || api.expectedETag != headers["If-Match"] {
		t.Fatalf("decision status=%d calls=%d etag=%q body=%s", decided.Code, api.decisionCalls, api.expectedETag, decided.Body.String())
	}
}

func TestDesignImportRemoteCapabilityErrorIsExplicit(t *testing.T) {
	api := &designImportAPIStub{createError: &designimport.Error{Kind: designimport.ErrCapabilityUnavailable, Field: "sourceUrl", Detail: "not configured"}}
	router := designImportRouter(t, api)
	body := `{
  "sourceKind":"figma","mode":"remote_url","sourceUrl":"https://www.figma.com/file/abc",
  "pageSpecRevision":{"artifactId":"a","revisionId":"r","contentHash":"sha256:h"}
}`
	response := designImportRequest(router, http.MethodPost, "/v1/projects/project-1/design-imports", body, map[string]string{"Idempotency-Key": "design-import-remote-1"})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("remote error status=%d body=%s", response.Code, response.Body.String())
	}
	var details map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil || details["code"] != "design_import_capability_unavailable" {
		t.Fatalf("unexpected problem payload: %#v err=%v", details, err)
	}
}

func TestDesignImportProcessingConflictIsRetryable(t *testing.T) {
	api := &designImportAPIStub{createError: designimport.ErrProcessing}
	router := designImportRouter(t, api)
	body := `{
  "sourceKind":"upload","mode":"upload",
  "pageSpecRevision":{"artifactId":"a","revisionId":"r","contentHash":"sha256:h"},
  "file":{"name":"design.json","mediaType":"application/json","contentBase64":"e30="}
}`
	response := designImportRequest(router, http.MethodPost, "/v1/projects/project-1/design-imports", body, map[string]string{"Idempotency-Key": "design-import-processing-1"})
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("processing status=%d retry=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
	var details map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil || details["code"] != "design_import_processing" {
		t.Fatalf("unexpected processing problem: %#v err=%v", details, err)
	}
}

func TestDesignImportProcessingResponseReleasesHTTPIdempotencyClaim(t *testing.T) {
	api := &designImportAPIStub{createErrors: []error{designimport.ErrProcessing, nil}}
	store := &memoryIdempotencyStore{}
	router := designImportRouterWithMutation(
		t, api,
		worksmiddleware.CaptureIdempotencyKey(true),
		worksmiddleware.PersistIdempotency(store),
	)
	body := `{
  "sourceKind":"upload","mode":"upload",
  "pageSpecRevision":{"artifactId":"a","revisionId":"r","contentHash":"sha256:h"},
  "file":{"name":"design.json","mediaType":"application/json","contentBase64":"e30="}
}`
	headers := map[string]string{"Idempotency-Key": "design-import-recover-same-command"}
	first := designImportRequest(router, http.MethodPost, "/v1/projects/project-1/design-imports", body, headers)
	second := designImportRequest(router, http.MethodPost, "/v1/projects/project-1/design-imports", body, headers)
	if first.Code != http.StatusServiceUnavailable || second.Code != http.StatusCreated || api.createCalls != 2 {
		t.Fatalf("first=%d second=%d calls=%d firstBody=%s secondBody=%s", first.Code, second.Code, api.createCalls, first.Body.String(), second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("retry unexpectedly replayed the processing response: %#v", second.Header())
	}
}

func TestDesignImportCreateRejectsUnknownFieldsBeforeService(t *testing.T) {
	api := &designImportAPIStub{}
	router := designImportRouter(t, api)
	body := `{"sourceKind":"upload","mode":"upload","credential":"secret"}`
	response := designImportRequest(router, http.MethodPost, "/v1/projects/project-1/design-imports", body, map[string]string{"Idempotency-Key": "design-import-unknown-1"})
	if response.Code != http.StatusBadRequest || api.createCalls != 0 {
		t.Fatalf("unknown field status=%d calls=%d body=%s", response.Code, api.createCalls, response.Body.String())
	}
}
