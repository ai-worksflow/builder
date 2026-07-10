package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	"github.com/worksflow/builder/backend/internal/health"
	"github.com/worksflow/builder/backend/internal/httpapi"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
)

const (
	testSessionCookie = "test_session"
	testCSRFCookie    = "test_csrf"
	testCSRFHeader    = "X-CSRF-Token"
)

var (
	testUserID    = uuid.NewString()
	testProjectID = uuid.NewString()
)

func TestProtectedRouteRejectsAnonymousRequestWithProblemDetails(t *testing.T) {
	router, _ := newTransportRouter(t, nil)
	response := performRequest(router, http.MethodGet, "/v1/projects", nil, nil)
	assertProblem(t, response, http.StatusUnauthorized, "authentication_required")
}

func TestCookieSessionCanListProjects(t *testing.T) {
	projects := &fakeProjectService{list: []core.Project{testProject(core.RoleOwner)}}
	router, _ := newTransportRouter(t, projects)
	response := performRequest(router, http.MethodGet, "/v1/projects", nil, authenticatedHeaders(false))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Items []map[string]interface{} `json:"items"`
	}
	decodeResponse(t, response, &body)
	if len(body.Items) != 1 || body.Items[0]["currentUserRole"] != "owner" {
		t.Fatalf("body = %#v", body)
	}
	if _, exists := body.Items[0]["teamId"]; exists {
		t.Fatalf("project response must not fabricate teamId: %#v", body.Items[0])
	}
}

func TestCookieMutationRequiresCSRF(t *testing.T) {
	projects := &fakeProjectService{}
	router, _ := newTransportRouter(t, projects)
	headers := authenticatedHeaders(false)
	headers.Set("Content-Type", "application/json")
	response := performRequest(router, http.MethodPost, "/v1/projects", []byte(`{"name":"Project"}`), headers)
	assertProblem(t, response, http.StatusForbidden, "csrf_failed")
	if projects.created {
		t.Fatal("project service was called before CSRF validation")
	}
}

func TestStrictJSONRejectsUnknownField(t *testing.T) {
	router, _ := newTransportRouter(t, &fakeProjectService{})
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	response := performRequest(router, http.MethodPost, "/v1/projects", []byte(`{"name":"Project","unknown":true}`), headers)
	assertProblem(t, response, http.StatusBadRequest, "unknown_json_field")
}

func TestServiceErrorsUseRFC9457ProblemDetails(t *testing.T) {
	projects := &fakeProjectService{getErr: core.ErrNotFound}
	router, _ := newTransportRouter(t, projects)
	response := performRequest(router, http.MethodGet, "/v1/projects/"+testProjectID, nil, authenticatedHeaders(false))
	assertProblem(t, response, http.StatusNotFound, "not_found")
	if response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
}

func TestProjectUpdateRequiresAndEnforcesETag(t *testing.T) {
	projects := &fakeProjectService{updateErr: core.ErrConflict}
	router, _ := newTransportRouter(t, projects)
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	withoutETag := performRequest(router, http.MethodPatch, "/v1/projects/"+testProjectID, []byte(`{"name":"Updated"}`), headers)
	assertProblem(t, withoutETag, http.StatusPreconditionRequired, "if_match_required")

	headers.Set("If-Match", `"project:`+testProjectID+`:3"`)
	withETag := performRequest(router, http.MethodPatch, "/v1/projects/"+testProjectID, []byte(`{"name":"Updated"}`), headers)
	assertProblem(t, withETag, http.StatusPreconditionFailed, "etag_mismatch")
	if projects.expectedVersion != 3 {
		t.Fatalf("expected version = %d, want 3", projects.expectedVersion)
	}
}

func TestAuthenticatedUserCreatesOwnerProject(t *testing.T) {
	projects := &fakeProjectService{createdProject: core.CreatedProject{
		Project: testProject(core.RoleOwner), InitialArtifactID: uuid.NewString(), InitialArtifactDraftID: uuid.NewString(),
	}}
	router, _ := newTransportRouter(t, projects)
	headers := authenticatedHeaders(true)
	headers.Set("Content-Type", "application/json")
	headers.Set("Idempotency-Key", "create-project-1")
	response := performRequest(router, http.MethodPost, "/v1/projects", []byte(`{"name":"Owner project","description":"Brief"}`), headers)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]interface{}
	decodeResponse(t, response, &body)
	if body["currentUserRole"] != "owner" || projects.actorID != testUserID {
		t.Fatalf("body = %#v, actor = %q", body, projects.actorID)
	}
	if response.Header().Get("ETag") == "" || response.Header().Get("X-Initial-Artifact-ID") == "" {
		t.Fatalf("response headers = %#v", response.Header())
	}
}

func TestRegistrationSetsSecureHttpOnlySessionAndCSRFToken(t *testing.T) {
	router, services := newTransportRouter(t, nil)
	services.auth.signUp = auth.IssuedSession{
		Session: testSession(), Token: "issued-token",
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	response := performRequest(router, http.MethodPost, "/v1/session/register", []byte(`{"displayName":"Owner","email":"owner@example.com","password":"password-123"}`), headers)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies = %#v", cookies)
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case testSessionCookie:
			sessionCookie = cookie
		case testCSRFCookie:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %#v", sessionCookie)
	}
	if csrfCookie == nil || csrfCookie.HttpOnly || !csrfCookie.Secure {
		t.Fatalf("CSRF cookie = %#v", csrfCookie)
	}
	var body map[string]interface{}
	decodeResponse(t, response, &body)
	if body["state"] != "authenticated" || body["csrfToken"] == "" {
		t.Fatalf("body = %#v", body)
	}
}

type fakeServices struct {
	auth     *fakeAuthService
	projects *fakeProjectService
	members  *fakeMemberService
	access   *fakeAccessControl
}

func newTransportRouter(t *testing.T, projects *fakeProjectService) (*gin.Engine, *fakeServices) {
	t.Helper()
	if projects == nil {
		projects = &fakeProjectService{}
	}
	services := &fakeServices{
		auth: &fakeAuthService{}, projects: projects,
		members: &fakeMemberService{}, access: &fakeAccessControl{},
	}
	cfg := testConfig()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := transport.NewServer(transport.Services{
		Auth: services.auth, Projects: services.projects, Members: services.members, Access: services.access,
	}, cfg, logger)
	router, err := httpapi.NewRouter(cfg, logger, httpapi.RouterOptions{
		Readiness: health.NewReadiness(time.Second, nil), Transport: api, Authentication: services.auth,
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	return router, services
}

func testConfig() config.Config {
	return config.Config{
		Environment: config.EnvironmentTest,
		ServiceName: "test-api",
		HTTP:        config.HTTPConfig{MaxJSONBodyBytes: 1 << 20},
		CORS: config.CORSConfig{
			AllowedOrigins: []string{"https://app.example.com"}, AllowedMethods: []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Content-Type", "If-Match", testCSRFHeader}, ExposedHeaders: []string{"ETag"},
		},
		Security: config.SecurityConfig{
			Session: config.SessionSecurityConfig{
				TTL: time.Hour, CookieName: testSessionCookie, CookiePath: "/", CookieSecure: true, CookieSameSite: "lax",
			},
			CSRF: config.CSRFConfig{CookieName: testCSRFCookie, HeaderName: testCSRFHeader, TokenBytes: 32},
		},
	}
}

func authenticatedHeaders(csrf bool) http.Header {
	headers := http.Header{"Cookie": []string{testSessionCookie + "=valid-token"}}
	if csrf {
		headers.Add("Cookie", testCSRFCookie+"=csrf-token")
		headers.Set(testCSRFHeader, "csrf-token")
	}
	return headers
}

func performRequest(router http.Handler, method, path string, body []byte, headers http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]interface{}
	decodeResponse(t, response, &body)
	if body["code"] != code || body["status"] != float64(status) || !strings.HasPrefix(body["type"].(string), "urn:worksflow:problem:") {
		t.Fatalf("problem = %#v", body)
	}
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, destination interface{}) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, response.Body.String())
	}
}

func testSession() auth.Session {
	return auth.Session{
		ID: uuid.NewString(), User: auth.User{
			ID: testUserID, Email: "owner@example.com", DisplayName: "Owner", CreatedAt: time.Now().Add(-time.Hour),
		}, ExpiresAt: time.Now().Add(time.Hour),
	}
}

func testProject(role core.Role) core.Project {
	return core.Project{
		ID: testProjectID, Name: "Project", Description: "Brief", Lifecycle: "active", Role: role,
		Version: 1, ETag: `"project:` + testProjectID + `:1"`, CreatedBy: testUserID,
		CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now(),
	}
}

type fakeAuthService struct {
	signUp auth.IssuedSession
}

func (f *fakeAuthService) SignUp(context.Context, string, string, string, string, string) (auth.IssuedSession, error) {
	if f.signUp.Token == "" {
		return auth.IssuedSession{Session: testSession(), Token: "issued-token"}, nil
	}
	return f.signUp, nil
}

func (*fakeAuthService) SignIn(context.Context, string, string, string, string) (auth.IssuedSession, error) {
	return auth.IssuedSession{Session: testSession(), Token: "issued-token"}, nil
}

func (*fakeAuthService) Authenticate(_ context.Context, token string) (auth.Session, error) {
	if token != "valid-token" {
		return auth.Session{}, auth.ErrSessionExpired
	}
	return testSession(), nil
}

func (*fakeAuthService) SignOut(context.Context, string) error { return nil }

type fakeProjectService struct {
	list            []core.Project
	getErr          error
	updateErr       error
	createdProject  core.CreatedProject
	created         bool
	actorID         string
	expectedVersion uint64
}

func (f *fakeProjectService) Create(_ context.Context, actorID string, _ core.CreateProjectInput) (core.CreatedProject, error) {
	f.created = true
	f.actorID = actorID
	if f.createdProject.Project.ID == "" {
		f.createdProject = core.CreatedProject{Project: testProject(core.RoleOwner)}
	}
	return f.createdProject, nil
}

func (f *fakeProjectService) List(context.Context, string) ([]core.Project, error) {
	return f.list, nil
}

func (f *fakeProjectService) Get(context.Context, string, string) (core.Project, error) {
	if f.getErr != nil {
		return core.Project{}, f.getErr
	}
	return testProject(core.RoleOwner), nil
}

func (f *fakeProjectService) Update(_ context.Context, _, _ string, expectedVersion uint64, _ core.UpdateProjectInput) (core.Project, error) {
	f.expectedVersion = expectedVersion
	if f.updateErr != nil {
		return core.Project{}, f.updateErr
	}
	return testProject(core.RoleOwner), nil
}

type fakeMemberService struct{}

func (*fakeMemberService) List(context.Context, string, string) ([]core.ProjectMember, error) {
	return nil, nil
}
func (*fakeMemberService) AddExisting(context.Context, string, string, string, core.Role) (core.ProjectMember, error) {
	return core.ProjectMember{}, nil
}
func (*fakeMemberService) Invite(context.Context, string, string, string, core.Role) (core.ProjectInvitation, error) {
	return core.ProjectInvitation{}, nil
}
func (*fakeMemberService) AcceptInvitation(context.Context, string, string) (core.ProjectMember, error) {
	return core.ProjectMember{}, nil
}
func (*fakeMemberService) UpdateRole(context.Context, string, string, string, core.Role, string) (core.ProjectMember, error) {
	return core.ProjectMember{}, nil
}
func (*fakeMemberService) Remove(context.Context, string, string, string, string) error { return nil }

type fakeAccessControl struct{}

func (*fakeAccessControl) Authorize(context.Context, string, string, core.Action) (core.Role, error) {
	return core.RoleOwner, nil
}
