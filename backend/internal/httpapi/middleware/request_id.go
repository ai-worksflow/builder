package middleware

import (
	"regexp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

const (
	RequestIDHeader = "X-Request-ID"
	RequestIDKey    = "request_id"
)

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func RequestID() gin.HandlerFunc {
	return func(context *gin.Context) {
		requestID := context.GetHeader(RequestIDHeader)
		if !validRequestID.MatchString(requestID) {
			requestID = uuid.NewString()
		}
		context.Set(RequestIDKey, requestID)
		context.Header(RequestIDHeader, requestID)
		context.Request = context.Request.WithContext(core.WithRequestID(context.Request.Context(), requestID))
		context.Next()
	}
}

func GetRequestID(context *gin.Context) string {
	value, _ := context.Get(RequestIDKey)
	requestID, _ := value.(string)
	return requestID
}
