package middleware

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

const (
	ifMatchKey         = "if_match"
	idempotencyKeyName = "idempotency_key"
)

var validIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:~-]{1,128}$`)

func RequireIfMatch() gin.HandlerFunc {
	return func(context *gin.Context) {
		value := strings.TrimSpace(context.GetHeader("If-Match"))
		if value == "" {
			problem.Write(context, problem.New(http.StatusPreconditionRequired, "if_match_required", "Precondition required", "This operation requires an If-Match header."))
			return
		}
		if value == "*" || strings.HasPrefix(value, "W/") || !strings.HasPrefix(value, `"`) || !strings.HasSuffix(value, `"`) {
			problem.Write(context, problem.New(http.StatusBadRequest, "invalid_if_match", "Invalid If-Match header", "If-Match must contain one strong entity tag."))
			return
		}
		context.Set(ifMatchKey, value)
		context.Next()
	}
}

func IfMatch(context *gin.Context) string {
	return context.GetString(ifMatchKey)
}

func CaptureIdempotencyKey(required bool) gin.HandlerFunc {
	return func(context *gin.Context) {
		value := strings.TrimSpace(context.GetHeader("Idempotency-Key"))
		if value == "" && !required {
			context.Next()
			return
		}
		if !validIdempotencyKey.MatchString(value) {
			problem.Write(context, problem.New(http.StatusBadRequest, "invalid_idempotency_key", "Invalid idempotency key", "Idempotency-Key must contain 1 to 128 safe ASCII characters."))
			return
		}
		context.Set(idempotencyKeyName, value)
		context.Next()
	}
}

func IdempotencyKey(context *gin.Context) string {
	return context.GetString(idempotencyKeyName)
}
