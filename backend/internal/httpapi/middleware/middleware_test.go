package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/core"
)

func TestRecoveryReturnsSafeError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID(), Recovery(slog.New(slog.NewTextHandler(io.Discard, nil))))
	router.GET("/panic", func(*gin.Context) { panic("sensitive internal detail") })

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get(RequestIDHeader) == "" {
		t.Fatal("recovery response is missing a request id")
	}
	if body := response.Body.String(); body == "" || strings.Contains(body, "sensitive internal detail") {
		t.Fatalf("recovery body = %q", body)
	}
}

func TestIdempotencyKeyIsValidatedAndStoredInContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID(), CaptureIdempotencyKey(true))
	router.POST("/", func(context *gin.Context) {
		context.String(http.StatusOK, IdempotencyKey(context))
	})

	validRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	validRequest.Header.Set("Idempotency-Key", "project-create_123")
	validResponse := httptest.NewRecorder()
	router.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusOK || validResponse.Body.String() != "project-create_123" {
		t.Fatalf("valid response = %d %q", validResponse.Code, validResponse.Body.String())
	}

	invalidRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	invalidRequest.Header.Set("Idempotency-Key", "contains spaces")
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest || !strings.Contains(invalidResponse.Body.String(), "invalid_idempotency_key") {
		t.Fatalf("invalid response = %d %q", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func TestRequestIDIsPropagatedToCoreContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/", func(context *gin.Context) {
		context.String(http.StatusOK, core.RequestIDFromContext(context.Request.Context()))
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, "audit-request-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Body.String() != "audit-request-1" {
		t.Fatalf("core request id = %q", response.Body.String())
	}
}

func TestBuilderCORSDefersPublicDataOriginsToDeploymentHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS(config.CORSConfig{
		AllowedOrigins: []string{"https://builder.example"}, AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Content-Type"}, MaxAge: time.Minute,
	}))
	router.GET("/v1/public/data/deployments/:deploymentId/tables", func(context *gin.Context) {
		context.Header("Access-Control-Allow-Origin", context.GetHeader("Origin"))
		context.Status(http.StatusNoContent)
	})
	router.GET("/v1/projects", func(context *gin.Context) { context.Status(http.StatusNoContent) })

	publicRequest := httptest.NewRequest(http.MethodGet, "/v1/public/data/deployments/deployment/tables", nil)
	publicRequest.Header.Set("Origin", "https://published.example")
	publicResponse := httptest.NewRecorder()
	router.ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusNoContent || publicResponse.Header().Get("Access-Control-Allow-Origin") != "https://published.example" {
		t.Fatalf("dynamic public data CORS was intercepted: %d %#v", publicResponse.Code, publicResponse.Header())
	}

	builderRequest := httptest.NewRequest(http.MethodGet, "/v1/projects", nil)
	builderRequest.Header.Set("Origin", "https://published.example")
	builderResponse := httptest.NewRecorder()
	router.ServeHTTP(builderResponse, builderRequest)
	if builderResponse.Code != http.StatusForbidden {
		t.Fatalf("builder CORS accepted a public application origin: %d", builderResponse.Code)
	}
}
