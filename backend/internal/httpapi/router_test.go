package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/health"
)

func TestHealthAndSecurityMiddleware(t *testing.T) {
	router := testRouter(t, health.NewReadiness(time.Second, nil))
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	request.Header.Set("X-Request-ID", "request-test-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Request-ID") != "request-test-1" {
		t.Fatalf("request id = %q", response.Header().Get("X-Request-ID"))
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("security headers = %#v", response.Header())
	}
}

func TestReadinessFailureReturnsServiceUnavailable(t *testing.T) {
	readiness := health.NewReadiness(time.Second, map[string]health.Check{
		"postgres": func(context.Context) error { return errors.New("down") },
	})
	router := testRouter(t, readiness)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCORSPreflight(t *testing.T) {
	router := testRouter(t, health.NewReadiness(time.Second, nil))
	request := httptest.NewRequest(http.MethodOptions, "/health/live", nil)
	request.Header.Set("Origin", "https://app.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("allow origin = %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestAttemptScopedModelGatewayRouteBypassesBrowserSessionMiddleware(t *testing.T) {
	cfg := config.Config{Environment: config.EnvironmentTest, ServiceName: "model-gateway-router-test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	called := false
	router, err := NewRouter(cfg, logger, RouterOptions{
		Readiness: health.NewReadiness(time.Second, nil),
		ModelGateway: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			called = true
			if request.Header.Get("Authorization") != "Bearer attempt-token" {
				t.Fatalf("capability header = %q", request.Header.Get("Authorization"))
			}
			writer.WriteHeader(http.StatusNoContent)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-model/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer attempt-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if !called || response.Code != http.StatusNoContent {
		t.Fatalf("Model Gateway called=%t status=%d", called, response.Code)
	}
}

func testRouter(t *testing.T, readiness *health.Readiness) http.Handler {
	t.Helper()
	cfg := config.Config{
		Environment: config.EnvironmentTest,
		ServiceName: "test-api",
		HTTP:        config.HTTPConfig{TrustedProxies: nil},
		CORS: config.CORSConfig{
			AllowedOrigins:   []string{"https://app.example.com"},
			AllowedMethods:   []string{"GET", "OPTIONS"},
			AllowedHeaders:   []string{"Content-Type"},
			ExposedHeaders:   []string{"X-Request-ID"},
			AllowCredentials: true,
			MaxAge:           time.Hour,
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router, err := NewRouter(cfg, logger, RouterOptions{
		Readiness: readiness,
		WebSocket: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNotImplemented)
		}),
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	return router
}
