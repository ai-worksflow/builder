package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

func AccessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(context *gin.Context) {
		startedAt := time.Now()
		context.Next()
		path := context.FullPath()
		if path == "" {
			path = context.Request.URL.Path
		}
		logger.Info("http request",
			"request_id", GetRequestID(context),
			"method", context.Request.Method,
			"path", path,
			"status", context.Writer.Status(),
			"response_bytes", context.Writer.Size(),
			"duration_ms", float64(time.Since(startedAt).Microseconds())/1000,
			"client_ip", context.ClientIP(),
		)
	}
}
