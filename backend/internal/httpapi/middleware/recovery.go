package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(context *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				requestID := GetRequestID(context)
				logger.Error("panic recovered",
					"request_id", requestID,
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				details := problem.New(http.StatusInternalServerError, "internal_error", "Internal server error", "An unexpected error occurred.")
				details.RequestID = requestID
				problem.Write(context, details)
			}
		}()
		context.Next()
	}
}
