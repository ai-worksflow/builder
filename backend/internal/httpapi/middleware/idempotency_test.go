package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

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

func TestCaptureWriterPreservesOriginalResponseAndBoundsReplay(t *testing.T) {
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
	if recorder.Body.String() != "abcdef" {
		t.Fatalf("original response = %q", recorder.Body.String())
	}
	if string(capture.Bytes()) != "abcd" || !capture.Overflowed() {
		t.Fatalf("capture = %q, overflow = %v", capture.Bytes(), capture.Overflowed())
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
