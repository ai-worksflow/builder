package middleware

import "github.com/gin-gonic/gin"

func SecurityHeaders(enableHSTS bool) gin.HandlerFunc {
	return func(context *gin.Context) {
		headers := context.Writer.Header()
		headers.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")
		headers.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if enableHSTS {
			headers.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		context.Next()
	}
}
