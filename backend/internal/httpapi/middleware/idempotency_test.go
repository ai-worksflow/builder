package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type idempotencyStoreTestStub struct {
	claim       ClaimResult
	claimErr    error
	completeErr error
	complete    int
	release     int
	seal        int
}

func (s *idempotencyStoreTestStub) Claim(context.Context, string, string, string) (ClaimResult, error) {
	return s.claim, s.claimErr
}

func (s *idempotencyStoreTestStub) Complete(context.Context, string, string, string, StoredResponse) error {
	s.complete++
	return s.completeErr
}

func (s *idempotencyStoreTestStub) Release(context.Context, string, string, string) error {
	s.release++
	return nil
}

func (s *idempotencyStoreTestStub) Seal(context.Context, string, string, string) error {
	s.seal++
	return nil
}

func (s *idempotencyStoreTestStub) MaxRequestBytes() int64 { return 1 << 20 }
func (s *idempotencyStoreTestStub) MaxResponseBytes() int  { return 1 << 20 }

func TestIdempotentRequestHashCanonicalizesQueryOrder(t *testing.T) {
	first := httptest.NewRequest(http.MethodPost, "/v1/projects/p/artifacts?tag=b&tag=a&page=2", nil)
	first.Header.Set("Content-Type", "application/json")
	second := httptest.NewRequest(http.MethodPost, "/v1/projects/p/artifacts?page=2&tag=a&tag=b", nil)
	second.Header.Set("Content-Type", "application/json")

	firstHash := hashIdempotentRequest(first, "user-1", []byte(`{"name":"one"}`))
	secondHash := hashIdempotentRequest(second, "user-1", []byte(`{"name":"one"}`))
	if firstHash != secondHash {
		t.Fatalf("canonical hashes differ: %q != %q", firstHash, secondHash)
	}
	if changed := hashIdempotentRequest(second, "user-1", []byte(`{"name":"two"}`)); changed == firstHash {
		t.Fatal("different request bodies produced the same hash")
	}
}

func TestIdempotencySeparatesPreconditionsAndHeaderScopedProjects(t *testing.T) {
	base := httptest.NewRequest(http.MethodPost, "/v1/data/connect/supabase", nil)
	base.Header.Set("Content-Type", "application/json")
	base.Header.Set("If-Match", `"state:1"`)
	base.Header.Set("X-Worksflow-Project-Id", "project-a")
	changedETag := base.Clone(base.Context())
	changedETag.Header = base.Header.Clone()
	changedETag.Header.Set("If-Match", `"state:2"`)
	changedProject := base.Clone(base.Context())
	changedProject.Header = base.Header.Clone()
	changedProject.Header.Set("X-Worksflow-Project-Id", "project-b")
	payload := []byte(`{"endpoint":"https://example.test"}`)
	baseHash := hashIdempotentRequest(base, "user-1", payload)
	if hashIdempotentRequest(changedETag, "user-1", payload) == baseHash {
		t.Fatal("If-Match was omitted from the idempotency request identity")
	}
	if hashIdempotentRequest(changedProject, "user-1", payload) == baseHash {
		t.Fatal("project scope header was omitted from the idempotency request identity")
	}
	gin.SetMode(gin.TestMode)
	contextA, _ := gin.CreateTestContext(httptest.NewRecorder())
	contextA.Request = base
	contextB, _ := gin.CreateTestContext(httptest.NewRecorder())
	contextB.Request = changedProject
	if idempotencyScope(contextA, "user-1") == idempotencyScope(contextB, "user-1") {
		t.Fatal("header-scoped project mutations shared an idempotency claim scope")
	}
}

func TestCaptureWriterBuffersOriginalResponseAndBoundsReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	base, ok := gin.CreateTestContextOnly(recorder, gin.New()).Writer.(gin.ResponseWriter)
	if !ok {
		t.Fatal("test writer does not implement gin.ResponseWriter")
	}
	capture := newCaptureWriter(base, 4)
	if _, err := capture.WriteString("abcdef"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("response escaped before durable completion: %q", recorder.Body.String())
	}
	if string(capture.Bytes()) != "abcd" || !capture.Overflowed() {
		t.Fatalf("capture = %q, overflow = %v", capture.Bytes(), capture.Overflowed())
	}
	capture.Commit(replayUnavailableResponse())
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "idempotency_response_unavailable") {
		t.Fatalf("bounded response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestSafeReplayHeadersExcludeCookiesAndRequestIdentity(t *testing.T) {
	headers := http.Header{
		"Content-Type":  []string{"application/json"},
		"Set-Cookie":    []string{"session=secret"},
		"X-Request-Id":  []string{"old-request"},
		"Authorization": []string{"Bearer secret"},
		"Etag":          []string{`"v2"`},
	}
	result := safeReplayHeaders(headers)
	if result.Get("Content-Type") != "application/json" || result.Get("ETag") != `"v2"` {
		t.Fatalf("safe headers = %#v", result)
	}
	if result.Get("Set-Cookie") != "" || result.Get("X-Request-ID") != "" || result.Get("Authorization") != "" {
		t.Fatalf("sensitive headers leaked: %#v", result)
	}
}

func TestPublicIdempotencyIdentityHashesCapabilityDeploymentAndCanonicalRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	identityFor := func(capabilityID, deploymentID, route, path string) idempotencyIdentity {
		router := gin.New()
		var result idempotencyIdentity
		router.PATCH(route, func(context *gin.Context) {
			result = publicMutationIdempotencyIdentity(context, publicIdempotencyIdentity{
				capabilityID: capabilityID,
				deploymentID: deploymentID,
			})
			context.Status(http.StatusNoContent)
		})
		request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"value":1}`))
		router.ServeHTTP(httptest.NewRecorder(), request)
		return result
	}
	canonicalRoute := "/v1/public/data/deployments/:deploymentId/tables/:tableId/records/:recordId"
	firstPath := "/v1/public/data/deployments/deployment-a/tables/table-a/records/record-a"
	first := identityFor("capability-a", "deployment-a", canonicalRoute, firstPath)
	same := identityFor("capability-a", "deployment-a", canonicalRoute, firstPath)
	otherCapability := identityFor("capability-b", "deployment-a", canonicalRoute, firstPath)
	otherDeployment := identityFor("capability-a", "deployment-b", canonicalRoute, "/v1/public/data/deployments/deployment-b/tables/table-a/records/record-a")
	otherResource := identityFor("capability-a", "deployment-a", canonicalRoute, "/v1/public/data/deployments/deployment-a/tables/table-b/records/record-a")
	otherCanonicalRoute := identityFor("capability-a", "deployment-a", "/v1/public/data/deployments/:deploymentId/:collection/:tableId/records/:recordId", firstPath)
	if first != same || first.scope == otherCapability.scope || first.scope == otherDeployment.scope || first.scope == otherResource.scope || first.scope == otherCanonicalRoute.scope {
		t.Fatalf("public idempotency identities were not isolated: first=%+v otherCapability=%+v otherDeployment=%+v otherResource=%+v otherCanonicalRoute=%+v", first, otherCapability, otherDeployment, otherResource, otherCanonicalRoute)
	}
	for _, secret := range []string{"capability-a", "deployment-a", "wfpub_secret"} {
		if strings.Contains(first.scope, secret) || strings.Contains(first.hashIdentity, secret) {
			t.Fatalf("public identity persisted raw capability material: scope=%q hashIdentity=%q", first.scope, first.hashIdentity)
		}
	}
}

func TestPublicIdempotencyInProgressAndCompletionFailureStayFailClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := func(store IdempotencyStore, handlerCalls *int) *httptest.ResponseRecorder {
		router := gin.New()
		router.POST("/public/:deploymentId",
			func(context *gin.Context) {
				if !SetPublicIdempotencyIdentity(context, "capability-a", context.Param("deploymentId")) {
					t.Fatal("failed to set test public identity")
				}
				context.Next()
			},
			CaptureIdempotencyKey(true),
			PersistPublicIdempotency(store),
			func(context *gin.Context) {
				*handlerCalls++
				context.JSON(http.StatusCreated, gin.H{"created": true})
			},
		)
		httpRequest := httptest.NewRequest(http.MethodPost, "/public/deployment-a", strings.NewReader(`{"value":1}`))
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Idempotency-Key", "public-write-1")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httpRequest)
		return response
	}

	inProgressStore := &idempotencyStoreTestStub{claimErr: ErrIdempotencyInProgress}
	inProgressCalls := 0
	inProgress := request(inProgressStore, &inProgressCalls)
	if inProgress.Code != http.StatusConflict || inProgress.Header().Get("Retry-After") != "1" || inProgressCalls != 0 {
		t.Fatalf("in-progress public request was not closed: status=%d calls=%d body=%s", inProgress.Code, inProgressCalls, inProgress.Body.String())
	}

	completionStore := &idempotencyStoreTestStub{claim: ClaimResult{Acquired: true}, completeErr: errors.New("database unavailable")}
	completionCalls := 0
	completion := request(completionStore, &completionCalls)
	if completion.Code != http.StatusServiceUnavailable || completionCalls != 1 || completionStore.seal != 1 || !strings.Contains(completion.Body.String(), "idempotency_completion_failed") {
		t.Fatalf("completion failure escaped fail-closed buffering: status=%d calls=%d seal=%d body=%s", completion.Code, completionCalls, completionStore.seal, completion.Body.String())
	}
}
