package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/dataruntime"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

type publicDataAPIStub struct {
	PublicDataRuntimeAPI
	capability       dataruntime.PublicCapability
	tables           []dataruntime.PublicTable
	origins          []string
	authCalls        int
	listCalls        int
	createCalls      int
	lastToken        string
	lastDeploymentID string
	policies         []dataruntime.PublicTablePolicy
	policyVersion    uint64
	putExpected      uint64
	deleteExpected   uint64
	putCalls         int
	deleteCalls      int
}

func (s *publicDataAPIStub) ListPolicies(context.Context, string, string) ([]dataruntime.PublicTablePolicy, error) {
	return append([]dataruntime.PublicTablePolicy(nil), s.policies...), nil
}

func (s *publicDataAPIStub) PutPolicy(_ context.Context, projectID, tableID, _ string, expectedVersion uint64, _ dataruntime.PublicTablePolicyInput) (dataruntime.PublicTablePolicy, error) {
	s.putCalls++
	s.putExpected = expectedVersion
	if expectedVersion != s.policyVersion {
		return dataruntime.PublicTablePolicy{}, dataruntime.PreconditionFailed("The public table policy changed since it was loaded")
	}
	s.policyVersion++
	policy := dataruntime.PublicTablePolicy{
		ProjectID: projectID, TableID: tableID, TableName: "messages", AllowRead: true,
		Version: s.policyVersion, ETag: dataruntime.PublicTablePolicyETag(projectID, tableID, s.policyVersion),
	}
	s.policies = []dataruntime.PublicTablePolicy{policy}
	return policy, nil
}

func (s *publicDataAPIStub) DeletePolicy(_ context.Context, _, _ string, _ string, expectedVersion uint64) error {
	s.deleteCalls++
	s.deleteExpected = expectedVersion
	if expectedVersion != s.policyVersion {
		return dataruntime.PreconditionFailed("The public table policy changed since it was loaded")
	}
	s.policies = nil
	s.policyVersion = 0
	return nil
}

func (s *publicDataAPIStub) Authenticate(_ context.Context, deploymentID, token string) (dataruntime.PublicCapability, error) {
	s.authCalls++
	s.lastToken = token
	s.lastDeploymentID = deploymentID
	return s.capability, nil
}

func (s *publicDataAPIStub) ValidateOrigin(dataruntime.PublicCapability, string) error { return nil }

func (s *publicDataAPIStub) PreflightOrigins(context.Context, string) ([]string, error) {
	return append([]string(nil), s.origins...), nil
}

func (s *publicDataAPIStub) ListPublicTables(context.Context, dataruntime.PublicCapability) ([]dataruntime.PublicTable, error) {
	s.listCalls++
	return s.tables, nil
}

func (s *publicDataAPIStub) CreatePublicRecord(context.Context, dataruntime.PublicCapability, string, string, dataruntime.RecordInput) (dataruntime.Record, error) {
	s.createCalls++
	return dataruntime.Record{ID: uuid.NewString()}, nil
}

type publicRateLimiterStub struct {
	decision dataruntime.PublicRateLimitDecision
	err      error
	calls    int
	request  dataruntime.PublicRateLimitRequest
}

type publicIdempotencyRecord struct {
	hash     string
	response *worksmiddleware.StoredResponse
}

type publicIdempotencyStoreStub struct {
	mu      sync.Mutex
	records map[string]publicIdempotencyRecord
	scopes  []string
}

func newPublicIdempotencyStoreStub() *publicIdempotencyStoreStub {
	return &publicIdempotencyStoreStub{records: map[string]publicIdempotencyRecord{}}
}

func (s *publicIdempotencyStoreStub) Claim(_ context.Context, scope, key, requestHash string) (worksmiddleware.ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopes = append(s.scopes, scope)
	recordKey := scope + "\x00" + key
	record, exists := s.records[recordKey]
	if !exists {
		s.records[recordKey] = publicIdempotencyRecord{hash: requestHash}
		return worksmiddleware.ClaimResult{Acquired: true}, nil
	}
	if record.hash != requestHash {
		return worksmiddleware.ClaimResult{}, worksmiddleware.ErrIdempotencyConflict
	}
	if record.response == nil {
		return worksmiddleware.ClaimResult{}, worksmiddleware.ErrIdempotencyInProgress
	}
	replay := *record.response
	replay.Headers = replay.Headers.Clone()
	replay.Body = append([]byte(nil), replay.Body...)
	return worksmiddleware.ClaimResult{Replay: &replay}, nil
}

func (s *publicIdempotencyStoreStub) Complete(_ context.Context, scope, key, requestHash string, response worksmiddleware.StoredResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recordKey := scope + "\x00" + key
	record := s.records[recordKey]
	if record.hash != requestHash {
		return worksmiddleware.ErrIdempotencyConflict
	}
	copyResponse := response
	copyResponse.Headers = response.Headers.Clone()
	copyResponse.Body = append([]byte(nil), response.Body...)
	record.response = &copyResponse
	s.records[recordKey] = record
	return nil
}

func (s *publicIdempotencyStoreStub) Release(_ context.Context, scope, key, requestHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recordKey := scope + "\x00" + key
	if s.records[recordKey].hash == requestHash {
		delete(s.records, recordKey)
	}
	return nil
}

func (s *publicIdempotencyStoreStub) Seal(context.Context, string, string, string) error {
	return nil
}

func (s *publicIdempotencyStoreStub) MaxRequestBytes() int64 { return 1 << 20 }
func (s *publicIdempotencyStoreStub) MaxResponseBytes() int  { return 1 << 20 }

func (l *publicRateLimiterStub) Allow(_ context.Context, request dataruntime.PublicRateLimitRequest) (dataruntime.PublicRateLimitDecision, error) {
	l.calls++
	l.request = request
	return l.decision, l.err
}

func newPublicDataTestRouter(t *testing.T, service PublicDataRuntimeAPI, limiter dataruntime.PublicRateLimiter, maxBody int64) *gin.Engine {
	return newPublicDataTestRouterWithStore(t, service, limiter, maxBody, newPublicIdempotencyStoreStub())
}

func newPublicDataTestRouterWithStore(t *testing.T, service PublicDataRuntimeAPI, limiter dataruntime.PublicRateLimiter, maxBody int64, idempotency worksmiddleware.IdempotencyStore) *gin.Engine {
	t.Helper()
	handler, err := NewPublicDataHandler(PublicDataDependencies{
		Service: service, RateLimiter: limiter, MaxJSONBodyBytes: maxBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	if err := RegisterPublicDataRoutes(router.Group("/v1"), handler, idempotency); err != nil {
		t.Fatal(err)
	}
	return router
}

func TestPublicDataRoutesUseBearerCapabilityOutsideBuilderSession(t *testing.T) {
	t.Parallel()

	deploymentID, capabilityID := uuid.NewString(), uuid.NewString()
	service := &publicDataAPIStub{
		capability: dataruntime.PublicCapability{ID: capabilityID, DeploymentID: deploymentID},
		tables:     []dataruntime.PublicTable{{ID: uuid.NewString(), Name: "messages"}},
	}
	limiter := &publicRateLimiterStub{decision: dataruntime.PublicRateLimitDecision{Allowed: true, Limit: 100, Remaining: 99}}
	router := newPublicDataTestRouter(t, service, limiter, 0)

	request := httptest.NewRequest(http.MethodGet, "/v1/public/data/deployments/"+deploymentID+"/tables", nil)
	request.Header.Set("Authorization", "Bearer wfpub_test-capability")
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	request.RemoteAddr = "192.0.2.20:4321"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.authCalls != 1 || service.listCalls != 1 || limiter.calls != 1 {
		t.Fatalf("public route failed: status=%d body=%s auth=%d list=%d limit=%d", response.Code, response.Body.String(), service.authCalls, service.listCalls, limiter.calls)
	}
	if service.lastDeploymentID != deploymentID || service.lastToken != "wfpub_test-capability" {
		t.Fatalf("capability binding was not forwarded: deployment=%s token=%s", service.lastDeploymentID, service.lastToken)
	}
	if limiter.request.ClientKey != "192.0.2.20" || limiter.request.ClientKey == "203.0.113.9" {
		t.Fatalf("untrusted forwarding header influenced rate key: %+v", limiter.request)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "https://app.example" || response.Header().Get("X-RateLimit-Remaining") != "99" {
		t.Fatalf("public CORS or rate headers missing: %v", response.Header())
	}
}

func TestPublicDataRoutesFailClosedWithoutCapabilityOrRedisBudget(t *testing.T) {
	t.Parallel()

	deploymentID := uuid.NewString()
	service := &publicDataAPIStub{capability: dataruntime.PublicCapability{ID: uuid.NewString(), DeploymentID: deploymentID}}
	limiter := &publicRateLimiterStub{decision: dataruntime.PublicRateLimitDecision{
		Allowed: false, Limit: 1, Remaining: 0, RetryAfter: 1500 * time.Millisecond,
	}}
	router := newPublicDataTestRouter(t, service, limiter, 0)

	unauthenticated := httptest.NewRequest(http.MethodGet, "/v1/public/data/deployments/"+deploymentID+"/tables", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	router.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized || service.authCalls != 0 || limiter.calls != 0 {
		t.Fatalf("missing bearer capability did not fail before service access: status=%d body=%s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
	}

	limited := httptest.NewRequest(http.MethodGet, "/v1/public/data/deployments/"+deploymentID+"/tables", nil)
	limited.Header.Set("Authorization", "Bearer wfpub_limited")
	limitedResponse := httptest.NewRecorder()
	router.ServeHTTP(limitedResponse, limited)
	if limitedResponse.Code != http.StatusTooManyRequests || limitedResponse.Header().Get("Retry-After") != "2" || service.listCalls != 0 {
		t.Fatalf("rate limit did not fail closed: status=%d retry=%q body=%s", limitedResponse.Code, limitedResponse.Header().Get("Retry-After"), limitedResponse.Body.String())
	}
}

func TestPublicDataMutationBodyIsStrictlyBounded(t *testing.T) {
	t.Parallel()

	deploymentID := uuid.NewString()
	service := &publicDataAPIStub{capability: dataruntime.PublicCapability{ID: uuid.NewString(), DeploymentID: deploymentID}}
	limiter := &publicRateLimiterStub{decision: dataruntime.PublicRateLimitDecision{Allowed: true, Limit: 10, Remaining: 9}}
	router := newPublicDataTestRouter(t, service, limiter, 128)
	body := `{"values":{"name":"` + strings.Repeat("x", 512) + `"}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/public/data/deployments/"+deploymentID+"/tables/"+uuid.NewString()+"/records", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer wfpub_bounded")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "bounded-create-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || service.createCalls != 0 {
		t.Fatalf("oversized public mutation reached service: status=%d body=%s creates=%d", response.Code, response.Body.String(), service.createCalls)
	}
}

func TestPublicDataMutationsRequireIdempotencyAndReplayByCapabilityScope(t *testing.T) {
	deploymentID, capabilityID, tableID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	token := "wfpub_plaintext-must-not-persist"
	service := &publicDataAPIStub{capability: dataruntime.PublicCapability{ID: capabilityID, DeploymentID: deploymentID}}
	limiter := &publicRateLimiterStub{decision: dataruntime.PublicRateLimitDecision{Allowed: true, Limit: 100, Remaining: 99}}
	store := newPublicIdempotencyStoreStub()
	router := newPublicDataTestRouterWithStore(t, service, limiter, 0, store)
	path := "/v1/public/data/deployments/" + deploymentID + "/tables/" + tableID + "/records"
	body := `{"values":{"name":"Ada"}}`

	missingKeyCases := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: path, body: body},
		{method: http.MethodPatch, path: path + "/record-a", body: body},
		{method: http.MethodDelete, path: path + "/record-a"},
	}
	for _, testCase := range missingKeyCases {
		missing := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
		missing.Header.Set("Authorization", "Bearer "+token)
		if testCase.body != "" {
			missing.Header.Set("Content-Type", "application/json")
		}
		missingResponse := httptest.NewRecorder()
		router.ServeHTTP(missingResponse, missing)
		if missingResponse.Code != http.StatusBadRequest {
			t.Fatalf("%s without idempotency key was not rejected: status=%d body=%s", testCase.method, missingResponse.Code, missingResponse.Body.String())
		}
	}
	if service.createCalls != 0 {
		t.Fatalf("missing idempotency key reached create mutation: calls=%d", service.createCalls)
	}

	request := func(payload string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "public-create-1")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	created := request(body)
	replayed := request(body)
	if created.Code != http.StatusCreated || replayed.Code != http.StatusCreated || replayed.Header().Get("Idempotency-Replayed") != "true" || service.createCalls != 1 {
		t.Fatalf("public replay failed: created=%d replay=%d header=%q calls=%d", created.Code, replayed.Code, replayed.Header().Get("Idempotency-Replayed"), service.createCalls)
	}
	if created.Body.String() != replayed.Body.String() || created.Header().Get("Location") != replayed.Header().Get("Location") {
		t.Fatalf("replay changed response: first=%s second=%s", created.Body.String(), replayed.Body.String())
	}
	conflict := request(`{"values":{"name":"Grace"}}`)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_key_conflict") || service.createCalls != 1 {
		t.Fatalf("same key with a different body did not conflict: status=%d body=%s calls=%d", conflict.Code, conflict.Body.String(), service.createCalls)
	}
	for _, scope := range store.scopes {
		if strings.Contains(scope, token) || strings.Contains(scope, capabilityID) || strings.Contains(scope, deploymentID) {
			t.Fatalf("public idempotency scope persisted sensitive identity: %q", scope)
		}
	}
}

func TestPublicDataPreflightAllowsOnlyConfiguredExplicitOrigin(t *testing.T) {
	t.Parallel()

	deploymentID := uuid.NewString()
	service := &publicDataAPIStub{origins: []string{"https://app.example"}}
	limiter := &publicRateLimiterStub{}
	router := newPublicDataTestRouter(t, service, limiter, 0)

	request := httptest.NewRequest(http.MethodOptions, "/v1/public/data/deployments/"+deploymentID+"/tables", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type, Idempotency-Key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "https://app.example" || !strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") || limiter.calls != 0 {
		t.Fatalf("configured preflight failed: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	denied := httptest.NewRequest(http.MethodOptions, "/v1/public/data/deployments/"+deploymentID+"/tables", nil)
	denied.Header.Set("Origin", "https://evil.example")
	denied.Header.Set("Access-Control-Request-Method", http.MethodGet)
	deniedResponse := httptest.NewRecorder()
	router.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden || deniedResponse.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unconfigured preflight origin was allowed: status=%d headers=%v", deniedResponse.Code, deniedResponse.Header())
	}
}

func TestPublicDataHandlerRequiresFailClosedLimiter(t *testing.T) {
	t.Parallel()

	if _, err := NewPublicDataHandler(PublicDataDependencies{Service: &publicDataAPIStub{}}); err == nil {
		t.Fatal("public handler accepted a missing rate limiter")
	}
}

type publicManagementAuthenticator struct{ userID string }

func (a publicManagementAuthenticator) Authenticate(context.Context, string) (auth.Session, error) {
	return auth.Session{
		ID: uuid.NewString(), User: auth.User{ID: a.userID, Email: "owner@example.com", DisplayName: "Owner"},
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func newPublicManagementRouter(t *testing.T, service PublicDataRuntimeAPI, userID string) *gin.Engine {
	t.Helper()
	handler, err := NewPublicDataHandler(PublicDataDependencies{
		Service: service, RateLimiter: &publicRateLimiterStub{}, MaxJSONBodyBytes: dataruntime.MaxPublicRequestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	security := config.SecurityConfig{Session: config.SessionSecurityConfig{CookieName: "session"}}
	protected := router.Group("/v1", worksmiddleware.RequireAuthentication(publicManagementAuthenticator{userID: userID}, security))
	if err := RegisterPublicDataManagementRoutes(protected, handler); err != nil {
		t.Fatal(err)
	}
	return router
}

func publicManagementRequest(router http.Handler, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer session-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestPublicPolicyManagementRequiresStrongETagAndRejectsLostUpdates(t *testing.T) {
	projectID, tableID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	initialETag := dataruntime.PublicTablePolicyETag(projectID, tableID, 1)
	service := &publicDataAPIStub{
		policyVersion: 1,
		policies: []dataruntime.PublicTablePolicy{{
			ProjectID: projectID, TableID: tableID, TableName: "messages", Version: 1, ETag: initialETag,
		}},
	}
	router := newPublicManagementRouter(t, service, userID)
	base := "/v1/data/projects/" + projectID + "/public-runtime/policies"

	listed := publicManagementRequest(router, http.MethodGet, base, "", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"version":1`) || !strings.Contains(listed.Body.String(), `"etag":`+strconv.Quote(initialETag)) {
		t.Fatalf("policy list omitted version/etag: status=%d body=%s", listed.Code, listed.Body.String())
	}
	body := `{"allowRead":true,"allowCreate":false,"allowUpdate":false,"allowDelete":false,"readableFields":["body"],"writableFields":[]}`
	missing := publicManagementRequest(router, http.MethodPut, base+"/"+tableID, body, "")
	if missing.Code != http.StatusPreconditionRequired || service.putCalls != 0 {
		t.Fatalf("missing If-Match reached service: status=%d calls=%d body=%s", missing.Code, service.putCalls, missing.Body.String())
	}
	updated := publicManagementRequest(router, http.MethodPut, base+"/"+tableID, body, initialETag)
	nextETag := dataruntime.PublicTablePolicyETag(projectID, tableID, 2)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != nextETag || service.putExpected != 1 {
		t.Fatalf("conditional update failed: status=%d expected=%d etag=%q body=%s", updated.Code, service.putExpected, updated.Header().Get("ETag"), updated.Body.String())
	}
	stale := publicManagementRequest(router, http.MethodPut, base+"/"+tableID, body, initialETag)
	if stale.Code != http.StatusPreconditionFailed || !strings.Contains(stale.Body.String(), "etag_mismatch") {
		t.Fatalf("stale update was not rejected: status=%d body=%s", stale.Code, stale.Body.String())
	}
	staleDelete := publicManagementRequest(router, http.MethodDelete, base+"/"+tableID, "", initialETag)
	if staleDelete.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale delete was not rejected: status=%d body=%s", staleDelete.Code, staleDelete.Body.String())
	}
	deleted := publicManagementRequest(router, http.MethodDelete, base+"/"+tableID, "", nextETag)
	if deleted.Code != http.StatusOK || service.deleteExpected != 2 {
		t.Fatalf("conditional delete failed: status=%d expected=%d body=%s", deleted.Code, service.deleteExpected, deleted.Body.String())
	}
}
