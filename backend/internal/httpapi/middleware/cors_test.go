package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/config"
)

func TestCORSPermitsDynamicSameOriginButRejectsCrossOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS(config.CORSConfig{
		AllowedOrigins: []string{"http://localhost:10000"}, AllowedMethods: []string{"POST"},
		AllowedHeaders: []string{"Content-Type"}, ExposedHeaders: []string{"ETag"},
		AllowCredentials: true, MaxAge: time.Hour,
	}))
	router.POST("/v1/projects/:projectId/presence/heartbeat", func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})

	same := httptest.NewRequest(http.MethodPost, "/v1/projects/p/presence/heartbeat", nil)
	same.Host = "43.216.1.58:10000"
	same.Header.Set("Origin", "http://43.216.1.58:10000")
	same.Header.Set("X-Forwarded-Proto", "http")
	sameResponse := httptest.NewRecorder()
	router.ServeHTTP(sameResponse, same)
	if sameResponse.Code != http.StatusNoContent || sameResponse.Header().Get("Access-Control-Allow-Origin") != "http://43.216.1.58:10000" {
		t.Fatalf("same-origin response = %d headers=%v", sameResponse.Code, sameResponse.Header())
	}

	cross := httptest.NewRequest(http.MethodPost, "/v1/projects/p/presence/heartbeat", nil)
	cross.Host = "43.216.1.58:10000"
	cross.Header.Set("Origin", "https://evil.example.com")
	cross.Header.Set("X-Forwarded-Proto", "http")
	crossResponse := httptest.NewRecorder()
	router.ServeHTTP(crossResponse, cross)
	if crossResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin response = %d", crossResponse.Code)
	}
}
