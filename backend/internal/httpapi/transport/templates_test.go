package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/templates"
)

const (
	templateRegistryActorID   = "10000000-0000-4000-8000-000000000001"
	templateRegistryReleaseID = "10000000-0000-4000-8000-000000000002"
	templateRegistryStackID   = "10000000-0000-4000-8000-000000000003"
	templateRegistryHash      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	templateRegistrySubject   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type templateRegistryStub struct {
	listReleaseCalls int
	getReleaseCalls  int
	getExactCalls    int
	listStackCalls   int
	getStackCalls    int
	getExactStack    int

	releaseOptions templates.TemplateReleaseListOptions
	releaseID      string
	releaseRef     templates.TemplateReleaseRef
	stackOptions   templates.FullStackTemplateListOptions
	stackID        string
	stackRef       templates.ExactFullStackTemplateRef

	listReleaseError error
	getReleaseError  error
	listStackError   error
	getStackError    error
}

func (s *templateRegistryStub) ListTemplateReleases(_ context.Context, options templates.TemplateReleaseListOptions) ([]templates.TemplateReleaseRegistration, error) {
	s.listReleaseCalls++
	s.releaseOptions = options
	return nil, s.listReleaseError
}

func (s *templateRegistryStub) GetTemplateRelease(_ context.Context, id string) (templates.TemplateReleaseRegistration, error) {
	s.getReleaseCalls++
	s.releaseID = id
	return templates.TemplateReleaseRegistration{}, s.getReleaseError
}

func (s *templateRegistryStub) GetTemplateReleaseExact(_ context.Context, ref templates.TemplateReleaseRef) (templates.TemplateReleaseRegistration, error) {
	s.getExactCalls++
	s.releaseRef = ref
	return templates.TemplateReleaseRegistration{}, s.getReleaseError
}

func (s *templateRegistryStub) ListFullStackTemplates(_ context.Context, options templates.FullStackTemplateListOptions) ([]templates.FullStackTemplateRegistration, error) {
	s.listStackCalls++
	s.stackOptions = options
	return nil, s.listStackError
}

func (s *templateRegistryStub) GetFullStackTemplate(_ context.Context, id string) (templates.FullStackTemplateRegistration, error) {
	s.getStackCalls++
	s.stackID = id
	return templates.FullStackTemplateRegistration{}, s.getStackError
}

func (s *templateRegistryStub) GetFullStackTemplateExact(_ context.Context, ref templates.ExactFullStackTemplateRef) (templates.FullStackTemplateRegistration, error) {
	s.getExactStack++
	s.stackRef = ref
	return templates.FullStackTemplateRegistration{}, s.getStackError
}

func (s *templateRegistryStub) ResolveForNewBuild(context.Context, templates.ExactFullStackTemplateRef) (templates.ResolvedFullStackTemplate, error) {
	return templates.ResolvedFullStackTemplate{}, errors.New("not exposed by the HTTP registry")
}

func templateRegistryRouter(t *testing.T, registry templates.RegistryReader, authenticated bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler, err := NewTemplateRegistryHandler(TemplateRegistryDependencies{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	group := router.Group("/v1")
	if authenticated {
		group.Use(func(c *gin.Context) {
			c.Set("platform_identity", worksmiddleware.Identity{
				Session:   auth.Session{User: auth.User{ID: templateRegistryActorID}},
				Transport: "bearer",
			})
			c.Next()
		})
	}
	if err := RegisterTemplateRegistryRoutes(group, handler); err != nil {
		t.Fatal(err)
	}
	return router
}

func templateRegistryRequest(router http.Handler, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestTemplateRegistryListsParseStrictFilters(t *testing.T) {
	registry := &templateRegistryStub{}
	router := templateRegistryRouter(t, registry, true)

	releases := templateRegistryRequest(router, "/v1/template-releases?templateId=react-web&limit=25&state=approved&state=revoked")
	if releases.Code != http.StatusOK || releases.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("release list status=%d cache=%q body=%s", releases.Code, releases.Header().Get("Cache-Control"), releases.Body.String())
	}
	if registry.listReleaseCalls != 1 || registry.releaseOptions.TemplateID != "react-web" || registry.releaseOptions.Limit != 25 ||
		len(registry.releaseOptions.States) != 2 || registry.releaseOptions.States[0] != templates.ReleaseApproved || registry.releaseOptions.States[1] != templates.ReleaseRevoked {
		t.Fatalf("release options = %#v calls=%d", registry.releaseOptions, registry.listReleaseCalls)
	}
	assertTemplateRegistryEmptyItems(t, releases)

	stacks := templateRegistryRequest(router, "/v1/full-stack-templates?templateId=react-fastapi&limit=10")
	if stacks.Code != http.StatusOK || registry.listStackCalls != 1 || registry.stackOptions.TemplateID != "react-fastapi" || registry.stackOptions.Limit != 10 {
		t.Fatalf("stack list status=%d options=%#v calls=%d body=%s", stacks.Code, registry.stackOptions, registry.listStackCalls, stacks.Body.String())
	}
	assertTemplateRegistryEmptyItems(t, stacks)
}

func TestTemplateRegistryRejectsUnknownDuplicateAndInvalidListQueries(t *testing.T) {
	tests := []string{
		"/v1/template-releases?cursor=next",
		"/v1/template-releases?limit=1&limit=2",
		"/v1/template-releases?limit=01",
		"/v1/template-releases?state=selectable",
		"/v1/template-releases?templateId=",
		"/v1/full-stack-templates?state=approved",
		"/v1/full-stack-templates?templateId=one&templateId=two",
	}
	for _, path := range tests {
		registry := &templateRegistryStub{}
		response := templateRegistryRequest(templateRegistryRouter(t, registry, true), path)
		assertTemplateRegistryProblem(t, response, http.StatusBadRequest, "invalid_query")
		if registry.listReleaseCalls != 0 || registry.listStackCalls != 0 {
			t.Fatalf("%s reached registry: release=%d stack=%d", path, registry.listReleaseCalls, registry.listStackCalls)
		}
	}
}

func TestTemplateRegistryReleaseDetailUsesIDOrCompleteExactIdentity(t *testing.T) {
	registry := &templateRegistryStub{}
	router := templateRegistryRouter(t, registry, true)

	byID := templateRegistryRequest(router, "/v1/template-releases/"+templateRegistryReleaseID)
	if byID.Code != http.StatusOK || registry.getReleaseCalls != 1 || registry.releaseID != templateRegistryReleaseID {
		t.Fatalf("ID get status=%d calls=%d id=%q body=%s", byID.Code, registry.getReleaseCalls, registry.releaseID, byID.Body.String())
	}
	exact := templateRegistryRequest(router, "/v1/template-releases/"+templateRegistryReleaseID+"?contentHash="+templateRegistryHash+"&subjectHash="+templateRegistrySubject)
	if exact.Code != http.StatusOK || registry.getExactCalls != 1 || registry.releaseRef != (templates.TemplateReleaseRef{
		ID: templateRegistryReleaseID, ContentHash: templateRegistryHash, SubjectHash: templateRegistrySubject,
	}) {
		t.Fatalf("exact get status=%d calls=%d ref=%#v body=%s", exact.Code, registry.getExactCalls, registry.releaseRef, exact.Body.String())
	}

	for _, query := range []string{"?contentHash=" + templateRegistryHash, "?subjectHash=" + templateRegistrySubject, "?contentHash=" + templateRegistryHash + "&contentHash=" + templateRegistryHash + "&subjectHash=" + templateRegistrySubject} {
		response := templateRegistryRequest(router, "/v1/template-releases/"+templateRegistryReleaseID+query)
		assertTemplateRegistryProblem(t, response, http.StatusBadRequest, "invalid_query")
	}
	if byID.Header().Get("Cache-Control") != "no-store" || exact.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("detail responses must be no-store")
	}
}

func TestTemplateRegistryFullStackDetailUsesOptionalExactHash(t *testing.T) {
	registry := &templateRegistryStub{}
	router := templateRegistryRouter(t, registry, true)

	byID := templateRegistryRequest(router, "/v1/full-stack-templates/"+templateRegistryStackID)
	exact := templateRegistryRequest(router, "/v1/full-stack-templates/"+templateRegistryStackID+"?contentHash="+templateRegistryHash)
	if byID.Code != http.StatusOK || registry.getStackCalls != 1 || registry.stackID != templateRegistryStackID {
		t.Fatalf("ID get status=%d calls=%d id=%q", byID.Code, registry.getStackCalls, registry.stackID)
	}
	if exact.Code != http.StatusOK || registry.getExactStack != 1 || registry.stackRef != (templates.ExactFullStackTemplateRef{ID: templateRegistryStackID, ContentHash: templateRegistryHash}) {
		t.Fatalf("exact get status=%d calls=%d ref=%#v", exact.Code, registry.getExactStack, registry.stackRef)
	}
	unknown := templateRegistryRequest(router, "/v1/full-stack-templates/"+templateRegistryStackID+"?subjectHash="+templateRegistrySubject)
	assertTemplateRegistryProblem(t, unknown, http.StatusBadRequest, "invalid_query")
}

func TestTemplateRegistryMapsDomainFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid", err: &templates.Error{Kind: templates.ErrInvalidTemplate, Code: "invalid_uuid", Field: "templateReleaseId", Detail: "must be a UUID"}, status: http.StatusBadRequest, code: "invalid_query"},
		{name: "not found", err: &templates.RegistryError{Kind: templates.ErrRegistryNotFound, Operation: "get", Resource: "template release", Detail: "record does not exist"}, status: http.StatusNotFound, code: "not_found"},
		{name: "integrity", err: &templates.RegistryError{Kind: templates.ErrRegistryIntegrity, Operation: "hydrate", Resource: "template release", Detail: "noncanonical"}, status: http.StatusInternalServerError, code: "template_registry_integrity"},
		{name: "unavailable", err: &templates.RegistryError{Kind: templates.ErrRegistryUnavailable, Operation: "list", Resource: "template releases", Detail: "database operation failed"}, status: http.StatusServiceUnavailable, code: "template_registry_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &templateRegistryStub{listReleaseError: test.err}
			response := templateRegistryRequest(templateRegistryRouter(t, registry, true), "/v1/template-releases")
			assertTemplateRegistryProblem(t, response, test.status, test.code)
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("cache control = %q", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestTemplateRegistryRequiresAuthenticatedIdentity(t *testing.T) {
	registry := &templateRegistryStub{}
	response := templateRegistryRequest(templateRegistryRouter(t, registry, false), "/v1/template-releases")
	assertTemplateRegistryProblem(t, response, http.StatusUnauthorized, "authentication_required")
	if registry.listReleaseCalls != 0 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthenticated call reached registry or was cacheable: calls=%d cache=%q", registry.listReleaseCalls, response.Header().Get("Cache-Control"))
	}
}

func TestTemplateRegistryHandlerRejectsMissingDependencies(t *testing.T) {
	if _, err := NewTemplateRegistryHandler(TemplateRegistryDependencies{}); err == nil {
		t.Fatal("expected missing registry error")
	}
	if err := RegisterTemplateRegistryRoutes(nil, &TemplateRegistryHandler{}); err == nil {
		t.Fatal("expected missing routes error")
	}
	if err := RegisterTemplateRegistryRoutes(gin.New(), nil); err == nil {
		t.Fatal("expected missing handler error")
	}
}

func assertTemplateRegistryEmptyItems(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Items == nil || len(payload.Items) != 0 {
		t.Fatalf("items payload=%#v err=%v body=%s", payload, err, response.Body.String())
	}
}

func assertTemplateRegistryProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var details map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil {
		t.Fatalf("decode problem: %v body=%s", err, response.Body.String())
	}
	if response.Code != status || details["code"] != code {
		t.Fatalf("problem status=%d code=%v want status=%d code=%s body=%s", response.Code, details["code"], status, code, response.Body.String())
	}
}
