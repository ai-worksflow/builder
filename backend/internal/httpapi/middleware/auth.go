package middleware

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

const identityKey = "platform_identity"

type SessionAuthenticator interface {
	Authenticate(context.Context, string) (auth.Session, error)
}

type Identity struct {
	Session   auth.Session
	Token     string
	Transport string
}

func RequireAuthentication(service SessionAuthenticator, cfg config.SecurityConfig) gin.HandlerFunc {
	return func(context *gin.Context) {
		identity, err := AuthenticateRequest(context, service, cfg)
		if err != nil {
			problem.WriteError(context, err)
			return
		}
		context.Set(identityKey, identity)
		context.Next()
	}
}

func AuthenticateRequest(context *gin.Context, service SessionAuthenticator, cfg config.SecurityConfig) (Identity, error) {
	if token, err := context.Cookie(cfg.Session.CookieName); err == nil && strings.TrimSpace(token) != "" {
		session, authErr := service.Authenticate(context.Request.Context(), token)
		return Identity{Session: session, Token: token, Transport: "cookie"}, authErr
	}
	authorization := strings.TrimSpace(context.GetHeader("Authorization"))
	if !strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		return Identity{}, auth.ErrSessionExpired
	}
	if context.GetHeader("Origin") != "" || context.GetHeader("Sec-Fetch-Site") != "" {
		return Identity{}, auth.ErrInvalidCredentials
	}
	token := strings.TrimSpace(authorization[len("Bearer "):])
	session, err := service.Authenticate(context.Request.Context(), token)
	return Identity{Session: session, Token: token, Transport: "bearer"}, err
}

func GetIdentity(context *gin.Context) (Identity, bool) {
	value, exists := context.Get(identityKey)
	identity, ok := value.(Identity)
	return identity, exists && ok
}

func RequireCSRF(cfg config.SecurityConfig) gin.HandlerFunc {
	return func(context *gin.Context) {
		identity, authenticated := GetIdentity(context)
		if authenticated && identity.Transport != "cookie" {
			context.Next()
			return
		}
		if !authenticated {
			if _, err := context.Cookie(cfg.Session.CookieName); err != nil {
				context.Next()
				return
			}
		}
		cookieToken, cookieErr := context.Cookie(cfg.CSRF.CookieName)
		headerToken := context.GetHeader(cfg.CSRF.HeaderName)
		if cookieErr != nil || cookieToken == "" || headerToken == "" ||
			subtle.ConstantTimeCompare([]byte(cookieToken), []byte(headerToken)) != 1 {
			problem.Write(context, problem.New(http.StatusForbidden, "csrf_failed", "CSRF validation failed", "A valid CSRF token is required for this request."))
			return
		}
		context.Next()
	}
}
