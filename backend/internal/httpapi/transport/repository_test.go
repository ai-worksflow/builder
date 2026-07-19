package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	repositoryTransportActor     = "20000000-0000-4000-8000-000000000001"
	repositoryTransportProject   = "20000000-0000-4000-8000-000000000002"
	repositoryTransportManifest  = "20000000-0000-4000-8000-000000000003"
	repositoryTransportCandidate = "20000000-0000-4000-8000-000000000004"
	repositoryTransportRebase    = "20000000-0000-4000-8000-000000000005"
	repositoryTransportConflict  = "20000000-0000-4000-8000-000000000006"
	repositoryTransportTarget    = "20000000-0000-4000-8000-000000000007"
	repositoryTransportSnapshot  = "20000000-0000-4000-8000-000000000008"
	repositoryTransportHash      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type repositoryAPIFake struct {
	bootstrapInput      repository.BootstrapCandidateInput
	bootstrapResult     repository.BootstrapCandidateResult
	bootstrapErr        error
	getProjectID        string
	getCandidateID      string
	getActorID          string
	getResult           repository.CandidateWorkspace
	getErr              error
	snapshotProjectID   string
	snapshotID          string
	snapshotContentHash string
	snapshotActorID     string
	snapshotResult      repository.RepositorySnapshotReceipt
	snapshotErr         error
	listProjectID       string
	listActorID         string
	listResult          repository.CandidateHeadList
	listErr             error
	searchInput         repository.CandidateSearchInput
	searchResult        repository.CandidateSearchResult
	searchErr           error
}

type candidateRebaseAPIFake struct {
	startInput    repository.StartCandidateRebaseInput
	startResult   repository.CandidateRebaseResult
	startErr      error
	getProjectID  string
	getRebaseID   string
	getActorID    string
	getResult     repository.CandidateRebaseResult
	getErr        error
	resolveInput  repository.ResolveCandidateRebaseConflictInput
	resolveResult repository.CandidateRebaseResult
	resolveErr    error
	contentResult repository.CandidateRebaseConflictContent
	contentErr    error
}

func (api *candidateRebaseAPIFake) Start(
	_ context.Context,
	input repository.StartCandidateRebaseInput,
) (repository.CandidateRebaseResult, error) {
	api.startInput = input
	return api.startResult, api.startErr
}

func (api *candidateRebaseAPIFake) Get(
	_ context.Context,
	projectID, rebaseID, actorID string,
) (repository.CandidateRebaseResult, error) {
	api.getProjectID, api.getRebaseID, api.getActorID = projectID, rebaseID, actorID
	return api.getResult, api.getErr
}

func (api *candidateRebaseAPIFake) ResolveConflict(
	_ context.Context,
	input repository.ResolveCandidateRebaseConflictInput,
) (repository.CandidateRebaseResult, error) {
	api.resolveInput = input
	return api.resolveResult, api.resolveErr
}

func (api *candidateRebaseAPIFake) ReadConflictContent(
	_ context.Context,
	_, _, _, _ string,
) (repository.CandidateRebaseConflictContent, error) {
	return api.contentResult, api.contentErr
}

func (api *repositoryAPIFake) Bootstrap(
	_ context.Context,
	input repository.BootstrapCandidateInput,
) (repository.BootstrapCandidateResult, error) {
	api.bootstrapInput = input
	return api.bootstrapResult, api.bootstrapErr
}

func (api *repositoryAPIFake) Get(
	_ context.Context,
	projectID, candidateID, actorID string,
) (repository.CandidateWorkspace, error) {
	api.getProjectID, api.getCandidateID, api.getActorID = projectID, candidateID, actorID
	return api.getResult, api.getErr
}

func (api *repositoryAPIFake) GetSnapshot(
	_ context.Context,
	projectID, snapshotID, contentHash, actorID string,
) (repository.RepositorySnapshotReceipt, error) {
	api.snapshotProjectID, api.snapshotID = projectID, snapshotID
	api.snapshotContentHash, api.snapshotActorID = contentHash, actorID
	return api.snapshotResult, api.snapshotErr
}

func (api *repositoryAPIFake) ListHeads(
	_ context.Context,
	projectID, actorID string,
) (repository.CandidateHeadList, error) {
	api.listProjectID, api.listActorID = projectID, actorID
	return api.listResult, api.listErr
}

func (api *repositoryAPIFake) SearchCandidate(
	_ context.Context,
	input repository.CandidateSearchInput,
) (repository.CandidateSearchResult, error) {
	api.searchInput = input
	return api.searchResult, api.searchErr
}

func repositoryTransportRouter(t *testing.T, api RepositoryAPI) *gin.Engine {
	return repositoryTransportRouterWithRebases(t, api, &candidateRebaseAPIFake{})
}

func repositoryTransportRouterWithRebases(
	t *testing.T,
	api RepositoryAPI,
	rebases CandidateRebaseAPI,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session: auth.Session{User: auth.User{ID: repositoryTransportActor}}, Transport: "bearer",
		})
		c.Next()
	})
	handler, err := NewRepositoryHandler(RepositoryDependencies{Service: api, Rebases: rebases})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterRepositoryRoutes(router, handler, worksmiddleware.CaptureIdempotencyKey(true)); err != nil {
		t.Fatal(err)
	}
	return router
}

func TestRepositoryBootstrapRequiresIdempotencyAndForwardsOnlyManifestIdentity(t *testing.T) {
	api := &repositoryAPIFake{bootstrapResult: repository.BootstrapCandidateResult{
		Created: true,
		Candidate: repository.CandidateWorkspace{
			ID: repositoryTransportCandidate, ProjectID: repositoryTransportProject,
		},
	}}
	router := repositoryTransportRouter(t, api)
	path := "/projects/" + repositoryTransportProject + "/repository-candidates"

	missingRequest := httptest.NewRequest(
		http.MethodPost, path, strings.NewReader(`{"buildManifestId":"`+repositoryTransportManifest+`"}`),
	)
	missingRequest.Header.Set("Content-Type", "application/json")
	missingResponse := httptest.NewRecorder()
	router.ServeHTTP(missingResponse, missingRequest)
	if missingResponse.Code != http.StatusBadRequest || api.bootstrapInput.OperationID != "" {
		t.Fatalf("missing idempotency status=%d input=%#v body=%s", missingResponse.Code, api.bootstrapInput, missingResponse.Body.String())
	}

	request := httptest.NewRequest(
		http.MethodPost, path, strings.NewReader(`{"buildManifestId":"`+repositoryTransportManifest+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "bootstrap-candidate-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated ||
		response.Header().Get("Location") != "/v1/projects/"+repositoryTransportProject+"/repository-candidates/"+repositoryTransportCandidate ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("bootstrap status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if api.bootstrapInput != (repository.BootstrapCandidateInput{
		ProjectID: repositoryTransportProject, BuildManifestID: repositoryTransportManifest,
		ActorID: repositoryTransportActor, OperationID: "bootstrap-candidate-1",
	}) {
		t.Fatalf("bootstrap input = %#v", api.bootstrapInput)
	}
}

func TestRepositoryTransportRejectsUnknownInputAndMapsBlockedBootstrap(t *testing.T) {
	unknownAPI := &repositoryAPIFake{}
	unknownRouter := repositoryTransportRouter(t, unknownAPI)
	unknown := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+repositoryTransportProject+"/repository-candidates",
		strings.NewReader(`{"buildManifestId":"`+repositoryTransportManifest+`","tree":[]}`),
	)
	unknown.Header.Set("Content-Type", "application/json")
	unknown.Header.Set("Idempotency-Key", "bootstrap-unknown")
	unknownResponse := httptest.NewRecorder()
	unknownRouter.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusBadRequest || unknownAPI.bootstrapInput.OperationID != "" {
		t.Fatalf("unknown field status=%d input=%#v body=%s", unknownResponse.Code, unknownAPI.bootstrapInput, unknownResponse.Body.String())
	}

	blockedAPI := &repositoryAPIFake{bootstrapErr: repository.ErrBootstrapNotReady}
	blockedRouter := repositoryTransportRouter(t, blockedAPI)
	blocked := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+repositoryTransportProject+"/repository-candidates",
		strings.NewReader(`{"buildManifestId":"`+repositoryTransportManifest+`"}`),
	)
	blocked.Header.Set("Content-Type", "application/json")
	blocked.Header.Set("Idempotency-Key", "bootstrap-blocked")
	blockedResponse := httptest.NewRecorder()
	blockedRouter.ServeHTTP(blockedResponse, blocked)
	var details map[string]any
	if err := json.Unmarshal(blockedResponse.Body.Bytes(), &details); err != nil ||
		blockedResponse.Code != http.StatusConflict || details["code"] != "repository_bootstrap_blocked" {
		t.Fatalf("blocked response status=%d payload=%#v err=%v", blockedResponse.Code, details, err)
	}
}

func TestRepositoryGetUsesProjectScopedAuthenticatedIdentity(t *testing.T) {
	api := &repositoryAPIFake{getResult: repository.CandidateWorkspace{
		ID: repositoryTransportCandidate, ProjectID: repositoryTransportProject,
	}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/projects/"+repositoryTransportProject+"/repository-candidates/"+repositoryTransportCandidate,
		nil,
	)
	response := httptest.NewRecorder()
	repositoryTransportRouter(t, api).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		api.getProjectID != repositoryTransportProject || api.getCandidateID != repositoryTransportCandidate ||
		api.getActorID != repositoryTransportActor {
		t.Fatalf("GET status=%d headers=%v identity=%s/%s/%s body=%s", response.Code, response.Header(), api.getProjectID, api.getCandidateID, api.getActorID, response.Body.String())
	}
}

func TestRepositorySnapshotGetRequiresExactHashAndAuthenticatedProject(t *testing.T) {
	api := &repositoryAPIFake{snapshotResult: repository.RepositorySnapshotReceipt{
		SchemaVersion: repository.RepositorySnapshotReceiptSchemaVersion,
		ContentHash:   repositoryTransportHash,
		Snapshot: repository.RepositorySnapshotReceiptSubject{
			ID: repositoryTransportSnapshot, ProjectID: repositoryTransportProject,
		},
	}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/projects/"+repositoryTransportProject+"/repository-snapshots/"+
			repositoryTransportSnapshot+"?contentHash="+repositoryTransportHash,
		nil,
	)
	response := httptest.NewRecorder()
	repositoryTransportRouter(t, api).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		api.snapshotProjectID != repositoryTransportProject || api.snapshotID != repositoryTransportSnapshot ||
		api.snapshotContentHash != repositoryTransportHash || api.snapshotActorID != repositoryTransportActor {
		t.Fatalf(
			"GET Snapshot status=%d headers=%v identity=%s/%s/%s/%s body=%s",
			response.Code, response.Header(), api.snapshotProjectID, api.snapshotID,
			api.snapshotContentHash, api.snapshotActorID, response.Body.String(),
		)
	}

	driftAPI := &repositoryAPIFake{snapshotErr: repository.ErrRepositorySnapshotDrift}
	driftResponse := httptest.NewRecorder()
	repositoryTransportRouter(t, driftAPI).ServeHTTP(driftResponse, request.Clone(request.Context()))
	var details map[string]any
	if err := json.Unmarshal(driftResponse.Body.Bytes(), &details); err != nil ||
		driftResponse.Code != http.StatusConflict || details["code"] != "repository_snapshot_content_changed" {
		t.Fatalf("Snapshot drift status=%d payload=%#v err=%v", driftResponse.Code, details, err)
	}

	invalidAPI := &repositoryAPIFake{snapshotErr: repository.ErrInvalidRepositorySnapshotSelection}
	invalidResponse := httptest.NewRecorder()
	repositoryTransportRouter(t, invalidAPI).ServeHTTP(invalidResponse, request.Clone(request.Context()))
	if err := json.Unmarshal(invalidResponse.Body.Bytes(), &details); err != nil ||
		invalidResponse.Code != http.StatusUnprocessableEntity ||
		details["code"] != "invalid_repository_snapshot_selection" {
		t.Fatalf("invalid Snapshot selection status=%d payload=%#v err=%v", invalidResponse.Code, details, err)
	}
}

func TestRepositoryCandidateHeadDiscoveryIsProjectScopedAndReadOnly(t *testing.T) {
	api := &repositoryAPIFake{listResult: repository.CandidateHeadList{
		SchemaVersion: "repository-candidate-head-list/v1",
		Candidates: []repository.CandidateHead{{
			Candidate: repository.CandidateWorkspace{
				ID: repositoryTransportCandidate, ProjectID: repositoryTransportProject,
			},
			RebaseID: repositoryTransportRebase,
		}},
	}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/projects/"+repositoryTransportProject+"/repository-candidates",
		nil,
	)
	response := httptest.NewRecorder()
	repositoryTransportRouter(t, api).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		api.listProjectID != repositoryTransportProject || api.listActorID != repositoryTransportActor {
		t.Fatalf("head discovery status=%d identity=%s/%s body=%s", response.Code,
			api.listProjectID, api.listActorID, response.Body.String())
	}
	var payload repository.CandidateHeadList
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Candidates) != 1 ||
		payload.Candidates[0].RebaseID != repositoryTransportRebase {
		t.Fatalf("head discovery payload=%#v err=%v", payload, err)
	}
}

func TestRepositoryCandidateSearchPinsExactHeadAndUsesReadRoute(t *testing.T) {
	rootHash := "sha256:" + strings.Repeat("a", 64)
	api := &repositoryAPIFake{searchResult: repository.CandidateSearchResult{
		SchemaVersion: repository.CandidateSearchSchemaVersion,
		ProjectID:     repositoryTransportProject,
		Head: repository.CandidateSearchHead{
			CandidateID: repositoryTransportCandidate, Generation: 7, RootHash: rootHash,
		},
		Matches: []repository.CandidateSearchMatch{{
			Path: "src/app.ts", Line: 3, Column: 5, Preview: "const exact = true",
			ContentHash: "sha256:" + strings.Repeat("b", 64),
		}},
	}}
	router := repositoryTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+repositoryTransportProject+"/repository-candidates/"+repositoryTransportCandidate+"/search",
		strings.NewReader(`{"expectedHeadGeneration":7,"expectedRootHash":"`+rootHash+`","query":"Exact","caseSensitive":false,"includeGlobs":["src/*.ts"],"maxMatches":25}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("search status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if api.searchInput.ProjectID != repositoryTransportProject ||
		api.searchInput.CandidateID != repositoryTransportCandidate ||
		api.searchInput.ActorID != repositoryTransportActor ||
		api.searchInput.ExpectedHeadGeneration != 7 || api.searchInput.ExpectedRootHash != rootHash ||
		api.searchInput.Query != "Exact" || api.searchInput.CaseSensitive ||
		len(api.searchInput.IncludeGlobs) != 1 || api.searchInput.IncludeGlobs[0] != "src/*.ts" ||
		api.searchInput.MaxMatches != 25 {
		t.Fatalf("search input = %#v", api.searchInput)
	}
	var payload repository.CandidateSearchResult
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Matches) != 1 ||
		payload.Head.Generation != 7 || payload.Matches[0].ContentHash == "" {
		t.Fatalf("search payload=%#v err=%v", payload, err)
	}
}

func TestRepositoryCandidateSearchDefaultsCaseSensitiveAndMapsFences(t *testing.T) {
	rootHash := "sha256:" + strings.Repeat("c", 64)
	api := &repositoryAPIFake{searchErr: repository.ErrCandidateSearchDrift}
	router := repositoryTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+repositoryTransportProject+"/repository-candidates/"+repositoryTransportCandidate+"/search",
		strings.NewReader(`{"expectedHeadGeneration":2,"expectedRootHash":"`+rootHash+`","query":"literal"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil ||
		response.Code != http.StatusConflict || payload["code"] != "repository_search_head_changed" ||
		!api.searchInput.CaseSensitive {
		t.Fatalf("drift status=%d input=%#v payload=%#v err=%v", response.Code, api.searchInput, payload, err)
	}

	invalidAPI := &repositoryAPIFake{searchErr: repository.ErrInvalidCandidateSearch}
	invalidRouter := repositoryTransportRouter(t, invalidAPI)
	invalid := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+repositoryTransportProject+"/repository-candidates/"+repositoryTransportCandidate+"/search",
		strings.NewReader(`{"expectedHeadGeneration":2,"expectedRootHash":"`+rootHash+`","query":"literal","maxMatches":501}`),
	)
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	invalidRouter.ServeHTTP(invalidResponse, invalid)
	if err := json.Unmarshal(invalidResponse.Body.Bytes(), &payload); err != nil ||
		invalidResponse.Code != http.StatusUnprocessableEntity || payload["code"] != "invalid_repository_search" {
		t.Fatalf("invalid status=%d payload=%#v err=%v", invalidResponse.Code, payload, err)
	}

	unknownAPI := &repositoryAPIFake{}
	unknownRouter := repositoryTransportRouter(t, unknownAPI)
	unknown := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+repositoryTransportProject+"/repository-candidates/"+repositoryTransportCandidate+"/search",
		strings.NewReader(`{"expectedHeadGeneration":2,"expectedRootHash":"`+rootHash+`","query":"literal","regex":true}`),
	)
	unknown.Header.Set("Content-Type", "application/json")
	unknownResponse := httptest.NewRecorder()
	unknownRouter.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusBadRequest || unknownAPI.searchInput.Query != "" {
		t.Fatalf("regex field was not rejected status=%d input=%#v body=%s", unknownResponse.Code, unknownAPI.searchInput, unknownResponse.Body.String())
	}
}

func TestRepositoryCandidateSearchMapsAdmissionQuotaAndUnsafeIndexFailures(t *testing.T) {
	rootHash := "sha256:" + strings.Repeat("d", 64)
	path := "/projects/" + repositoryTransportProject + "/repository-candidates/" +
		repositoryTransportCandidate + "/search"
	body := `{"expectedHeadGeneration":2,"expectedRootHash":"` + rootHash + `","query":"literal"}`
	var nilDenial *repository.ExactTreeSearchAdmissionDeniedError
	for _, fixture := range []struct {
		name       string
		err        error
		status     int
		code       string
		retryAfter string
	}{
		{
			name: "query rate denial",
			err: &repository.ExactTreeSearchAdmissionDeniedError{
				Operation:  repository.ExactTreeSearchAdmissionQuery,
				RetryAfter: 1500 * time.Millisecond,
			},
			status: http.StatusTooManyRequests, code: "repository_search_rate_limited", retryAfter: "2",
		},
		{
			name: "admission outage", err: repository.ErrExactTreeSearchAdmissionUnavailable,
			status: http.StatusServiceUnavailable, code: "repository_search_admission_unavailable",
		},
		{
			name: "admission invalid", err: repository.ErrExactTreeSearchAdmissionInvalid,
			status: http.StatusServiceUnavailable, code: "repository_search_admission_unavailable",
		},
		{
			name: "bare admission denial", err: repository.ErrExactTreeSearchAdmissionDenied,
			status: http.StatusServiceUnavailable, code: "repository_search_admission_unavailable",
		},
		{
			name: "first-builder rate denial",
			err: &repository.ExactTreeSearchAdmissionDeniedError{
				Operation: repository.ExactTreeSearchAdmissionFirstBuilder, RetryAfter: time.Second,
			},
			status: http.StatusTooManyRequests, code: "repository_search_rate_limited", retryAfter: "1",
		},
		{
			name: "unknown-operation admission denial",
			err: &repository.ExactTreeSearchAdmissionDeniedError{
				Operation: "other", RetryAfter: time.Second,
			},
			status: http.StatusServiceUnavailable, code: "repository_search_admission_unavailable",
		},
		{
			name: "zero-retry admission denial",
			err: &repository.ExactTreeSearchAdmissionDeniedError{
				Operation: repository.ExactTreeSearchAdmissionQuery,
			},
			status: http.StatusServiceUnavailable, code: "repository_search_admission_unavailable",
		},
		{
			name: "negative-retry admission denial",
			err: &repository.ExactTreeSearchAdmissionDeniedError{
				Operation: repository.ExactTreeSearchAdmissionQuery, RetryAfter: -time.Nanosecond,
			},
			status: http.StatusServiceUnavailable, code: "repository_search_admission_unavailable",
		},
		{
			name: "oversized-retry admission denial",
			err: &repository.ExactTreeSearchAdmissionDeniedError{
				Operation: repository.ExactTreeSearchAdmissionQuery, RetryAfter: time.Hour + time.Nanosecond,
			},
			status: http.StatusServiceUnavailable, code: "repository_search_admission_unavailable",
		},
		{
			name: "nil typed admission denial", err: nilDenial,
			status: http.StatusServiceUnavailable, code: "repository_search_admission_unavailable",
		},
		{
			name: "invalid index", err: repository.ErrInvalidExactTreeLiteralIndex,
			status: http.StatusServiceUnavailable, code: "repository_search_index_unavailable",
		},
		{
			name: "index infrastructure outage", err: repository.ErrCandidateSearchIndexUnavailable,
			status: http.StatusServiceUnavailable, code: "repository_search_index_unavailable",
		},
		{
			name: "mixed denial and claim release",
			err: errors.Join(
				&repository.ExactTreeSearchAdmissionDeniedError{
					Operation: repository.ExactTreeSearchAdmissionFirstBuilder, RetryAfter: time.Second,
				},
				repository.ErrExactTreeLiteralClaimRelease,
			),
			status: http.StatusServiceUnavailable, code: "repository_search_index_unavailable",
		},
		{
			name: "active build quota", err: repository.ErrExactTreeLiteralProjectActiveBuildQuota,
			status: http.StatusTooManyRequests, code: "repository_search_index_busy", retryAfter: "1",
		},
		{
			name: "tree quota", err: repository.ErrExactTreeLiteralProjectTreeQuota,
			status: http.StatusConflict, code: "repository_search_index_quota_exceeded",
		},
		{
			name: "source byte quota", err: repository.ErrExactTreeLiteralProjectSourceBytesQuota,
			status: http.StatusConflict, code: "repository_search_index_quota_exceeded",
		},
		{
			name: "tampered index", err: repository.ErrExactTreeLiteralIndexConflict,
			status: http.StatusServiceUnavailable, code: "repository_search_index_unavailable",
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			api := &repositoryAPIFake{searchErr: fixture.err}
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			repositoryTransportRouter(t, api).ServeHTTP(response, request)
			var payload map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil ||
				response.Code != fixture.status || payload["code"] != fixture.code ||
				response.Header().Get("Retry-After") != fixture.retryAfter {
				t.Fatalf("status=%d retry=%q payload=%#v err=%v", response.Code,
					response.Header().Get("Retry-After"), payload, err)
			}
		})
	}
}

func TestRepositoryRebaseRoutesForwardOnlyExactFencesAndResolution(t *testing.T) {
	candidates := &repositoryAPIFake{}
	rebases := &candidateRebaseAPIFake{startResult: repository.CandidateRebaseResult{
		Created:   true,
		Rebase:    repository.CandidateRebase{ID: repositoryTransportRebase, ProjectID: repositoryTransportProject},
		Candidate: repository.CandidateWorkspace{ID: repositoryTransportTarget, ProjectID: repositoryTransportProject},
	}}
	router := repositoryTransportRouterWithRebases(t, candidates, rebases)
	start := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+repositoryTransportProject+"/repository-candidates/"+repositoryTransportCandidate+"/rebases",
		strings.NewReader(`{"targetBuildManifestId":"`+repositoryTransportManifest+`","expectedCandidateVersion":9,"expectedSessionEpoch":3,"expectedWriterLeaseEpoch":4}`),
	)
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("Idempotency-Key", "rebase-exact-1")
	startResponse := httptest.NewRecorder()
	router.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusCreated ||
		startResponse.Header().Get("Location") != "/v1/projects/"+repositoryTransportProject+"/candidate-rebases/"+repositoryTransportRebase {
		t.Fatalf("start rebase status=%d headers=%v body=%s", startResponse.Code, startResponse.Header(), startResponse.Body.String())
	}
	if rebases.startInput != (repository.StartCandidateRebaseInput{
		ProjectID: repositoryTransportProject, PredecessorCandidateID: repositoryTransportCandidate,
		TargetBuildManifestID: repositoryTransportManifest, ExpectedCandidateVersion: 9,
		ExpectedSessionEpoch: 3, ExpectedWriterLeaseEpoch: 4,
		ActorID: repositoryTransportActor, OperationID: "rebase-exact-1",
	}) {
		t.Fatalf("start rebase input = %#v", rebases.startInput)
	}

	custom := ""
	rebases.resolveResult = repository.CandidateRebaseResult{
		Rebase: repository.CandidateRebase{ID: repositoryTransportRebase, ProjectID: repositoryTransportProject},
	}
	resolve := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+repositoryTransportProject+"/candidate-rebases/"+repositoryTransportRebase+
			"/conflicts/"+repositoryTransportConflict+"/resolve",
		strings.NewReader(`{"expectedConflictVersion":1,"strategy":"current","content":"","mode":"100755"}`),
	)
	resolve.Header.Set("Content-Type", "application/json")
	resolve.Header.Set("Idempotency-Key", "resolve-exact-1")
	resolveResponse := httptest.NewRecorder()
	router.ServeHTTP(resolveResponse, resolve)
	if resolveResponse.Code != http.StatusOK || rebases.resolveInput.ProjectID != repositoryTransportProject ||
		rebases.resolveInput.RebaseID != repositoryTransportRebase ||
		rebases.resolveInput.ConflictID != repositoryTransportConflict ||
		rebases.resolveInput.ExpectedConflictVersion != 1 ||
		rebases.resolveInput.Strategy != repository.CandidateRebaseUseCurrent ||
		rebases.resolveInput.Content == nil || *rebases.resolveInput.Content != custom ||
		rebases.resolveInput.Mode != "100755" || rebases.resolveInput.ActorID != repositoryTransportActor {
		t.Fatalf("resolve status=%d input=%#v body=%s", resolveResponse.Code, rebases.resolveInput, resolveResponse.Body.String())
	}

	rebases.getResult = repository.CandidateRebaseResult{
		Rebase: repository.CandidateRebase{ID: repositoryTransportRebase, ProjectID: repositoryTransportProject},
	}
	get := httptest.NewRequest(
		http.MethodGet,
		"/projects/"+repositoryTransportProject+"/candidate-rebases/"+repositoryTransportRebase,
		nil,
	)
	getResponse := httptest.NewRecorder()
	router.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || rebases.getProjectID != repositoryTransportProject ||
		rebases.getRebaseID != repositoryTransportRebase || rebases.getActorID != repositoryTransportActor {
		t.Fatalf("get rebase status=%d identity=%s/%s/%s body=%s", getResponse.Code,
			rebases.getProjectID, rebases.getRebaseID, rebases.getActorID, getResponse.Body.String())
	}
}

func TestRepositoryHandlerRequiresDependencies(t *testing.T) {
	if _, err := NewRepositoryHandler(RepositoryDependencies{}); err == nil {
		t.Fatal("expected missing service error")
	}
	if err := RegisterRepositoryRoutes(nil, &RepositoryHandler{}); err == nil {
		t.Fatal("expected missing routes error")
	}
	if err := RegisterRepositoryRoutes(gin.New(), nil); err == nil {
		t.Fatal("expected missing handler error")
	}
}
