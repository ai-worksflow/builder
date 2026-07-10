package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

func CORS(cfg config.CORSConfig) gin.HandlerFunc {
	allowedOrigins := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, origin := range cfg.AllowedOrigins {
		allowedOrigins[origin] = struct{}{}
	}
	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	exposed := strings.Join(cfg.ExposedHeaders, ", ")
	maxAge := strconv.FormatInt(int64(cfg.MaxAge.Seconds()), 10)

	return func(context *gin.Context) {
		origin := context.GetHeader("Origin")
		if origin == "" {
			context.Next()
			return
		}
		_, exactMatch := allowedOrigins[origin]
		_, wildcard := allowedOrigins["*"]
		if !exactMatch && !wildcard {
			problem.Write(context, problem.New(http.StatusForbidden, "origin_forbidden", "Origin forbidden", "Origin is not allowed."))
			return
		}

		responseHeaders := context.Writer.Header()
		responseHeaders.Add("Vary", "Origin")
		if wildcard {
			responseHeaders.Set("Access-Control-Allow-Origin", "*")
		} else {
			responseHeaders.Set("Access-Control-Allow-Origin", origin)
		}
		responseHeaders.Set("Access-Control-Allow-Methods", methods)
		responseHeaders.Set("Access-Control-Allow-Headers", headers)
		responseHeaders.Set("Access-Control-Expose-Headers", exposed)
		responseHeaders.Set("Access-Control-Max-Age", maxAge)
		if cfg.AllowCredentials {
			responseHeaders.Set("Access-Control-Allow-Credentials", "true")
		}
		if context.Request.Method == http.MethodOptions {
			context.AbortWithStatus(http.StatusNoContent)
			return
		}
		context.Next()
	}
}
