package transport

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type registerSessionInput struct {
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

type loginSessionInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type sessionResponse struct {
	State     string     `json:"state"`
	User      *auth.User `json:"user,omitempty"`
	SessionID string     `json:"sessionId,omitempty"`
	IssuedAt  *time.Time `json:"issuedAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CSRFToken string     `json:"csrfToken,omitempty"`
}

func (s *Server) RegisterSession(context *gin.Context) {
	noStore(context)
	var input registerSessionInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	idempotent, ok := s.services.Auth.(IdempotentAuthService)
	if !ok {
		writeAuthIdempotencyError(context, auth.ErrIdempotencyUnavailable)
		return
	}
	issued, err := idempotent.SignUpIdempotent(
		context.Request.Context(), worksmiddleware.IdempotencyKey(context),
		input.Email, input.DisplayName, input.Password, context.Request.UserAgent(), context.ClientIP(),
	)
	if err != nil {
		if writeAuthIdempotencyError(context, err) {
			return
		}
		if registrationInputError(err) {
			problem.Write(context, problem.New(http.StatusUnprocessableEntity, "invalid_input", "Input is invalid", "The display name, email, or password does not satisfy the account policy."))
			return
		}
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	s.writeIdempotentIssuedSession(context, issued, http.StatusCreated)
}

func (s *Server) LoginSession(context *gin.Context) {
	noStore(context)
	var input loginSessionInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	idempotent, ok := s.services.Auth.(IdempotentAuthService)
	if !ok {
		writeAuthIdempotencyError(context, auth.ErrIdempotencyUnavailable)
		return
	}
	issued, err := idempotent.SignInIdempotent(
		context.Request.Context(), worksmiddleware.IdempotencyKey(context),
		input.Email, input.Password, context.Request.UserAgent(), context.ClientIP(),
	)
	if err != nil {
		if writeAuthIdempotencyError(context, err) {
			return
		}
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	s.writeIdempotentIssuedSession(context, issued, http.StatusOK)
}

func (s *Server) GetSession(context *gin.Context) {
	noStore(context)
	if !requestHasSessionCredential(context, s.config.Security.Session.CookieName) {
		context.JSON(http.StatusOK, sessionResponse{State: "anonymous"})
		return
	}
	identity, err := worksmiddleware.AuthenticateRequest(context, s.services.Auth, s.config.Security)
	if err != nil {
		s.clearSessionCookies(context)
		context.JSON(http.StatusOK, sessionResponse{State: "expired"})
		return
	}
	csrfToken := ""
	if identity.Transport == "cookie" {
		csrfToken = s.ensureCSRFCookie(context, identity.Session.ExpiresAt)
	}
	context.JSON(http.StatusOK, responseForSession(identity.Session, csrfToken, nil))
}

func (s *Server) RefreshSession(context *gin.Context) {
	noStore(context)
	if !refreshBodyIsEmpty(context) {
		return
	}
	if token, err := context.Cookie(s.config.Security.Session.CookieName); err == nil && strings.TrimSpace(token) != "" {
		csrfToken, csrfErr := context.Cookie(s.config.Security.CSRF.CookieName)
		if csrfErr != nil || strings.TrimSpace(csrfToken) == "" {
			problem.Write(context, problem.New(http.StatusForbidden, "csrf_failed", "CSRF validation failed", "A valid CSRF token is required for this request."))
			return
		}
		idempotent, ok := s.services.Auth.(IdempotentAuthService)
		if !ok {
			writeAuthIdempotencyError(context, auth.ErrIdempotencyUnavailable)
			return
		}
		issued, err := idempotent.RotateIdempotent(
			context.Request.Context(), worksmiddleware.IdempotencyKey(context), token, csrfToken,
			context.Request.UserAgent(), context.ClientIP(),
		)
		if err != nil {
			if writeAuthIdempotencyError(context, err) {
				return
			}
			if errors.Is(err, auth.ErrSessionExpired) || errors.Is(err, auth.ErrUserDisabled) {
				s.clearSessionCookies(context)
			}
			problem.WriteError(context, err)
			return
		}
		s.writeIdempotentIssuedSession(context, issued, http.StatusOK)
		return
	}
	identity, err := worksmiddleware.AuthenticateRequest(context, s.services.Auth, s.config.Security)
	if err != nil {
		problem.WriteError(context, err)
		return
	}
	context.JSON(http.StatusOK, responseForSession(identity.Session, "", nil))
}

func (s *Server) LogoutSession(context *gin.Context) {
	noStore(context)
	identity, ok := worksmiddleware.GetIdentity(context)
	if !ok {
		var err error
		identity, err = worksmiddleware.AuthenticateRequest(context, s.services.Auth, s.config.Security)
		ok = err == nil
	}
	if ok {
		if err := s.services.Auth.SignOut(context.Request.Context(), identity.Token); err != nil {
			s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
			problem.WriteError(context, err)
			return
		}
	}
	s.clearSessionCookies(context)
	context.Status(http.StatusNoContent)
}

func (s *Server) writeIdempotentIssuedSession(context *gin.Context, issued auth.IdempotentIssuedSession, status int) {
	// Expires is stable across a replay. Omitting Max-Age avoids both changing
	// the replay header and accidentally extending the server-side session when
	// an old receipt is replayed later.
	s.setIdempotentCookie(context, s.config.Security.Session.CookieName, issued.Token, true, issued.ExpiresAt)
	s.setIdempotentCookie(context, s.config.Security.CSRF.CookieName, issued.CSRFToken, false, issued.ExpiresAt)
	issuedAt := issued.IssuedAt
	if issued.Replayed {
		context.Header("Idempotency-Replayed", "true")
	}
	context.JSON(status, responseForSession(issued.Session, issued.CSRFToken, &issuedAt))
}

func refreshBodyIsEmpty(context *gin.Context) bool {
	if context.Request.Body == nil {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(context.Request.Body, 1025))
	if err != nil || len(body) > 1024 || strings.TrimSpace(string(body)) != "" {
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_refresh_request", "Invalid refresh request", "The refresh request body must be empty."))
		return false
	}
	return true
}

func writeAuthIdempotencyError(context *gin.Context, err error) bool {
	switch {
	case errors.Is(err, auth.ErrIdempotencyConflict):
		problem.Write(context, problem.New(http.StatusConflict, "idempotency_key_conflict", "Idempotency key conflict", "This idempotency key was already used for a different authentication request."))
	case errors.Is(err, auth.ErrIdempotencyInProgress):
		context.Header("Retry-After", "1")
		problem.Write(context, problem.New(http.StatusConflict, "idempotency_request_in_progress", "Request is in progress", "An authentication request with this idempotency key is still being processed."))
	case errors.Is(err, auth.ErrIdempotencyUnavailable):
		problem.Write(context, problem.New(http.StatusServiceUnavailable, "idempotency_unavailable", "Request protection unavailable", "The authentication request could not be safely committed. Retry later with the same key."))
	default:
		return false
	}
	return true
}

func responseForSession(session auth.Session, csrfToken string, issuedAt *time.Time) sessionResponse {
	user := session.User
	expiresAt := session.ExpiresAt
	return sessionResponse{
		State: "authenticated", User: &user, SessionID: session.ID,
		IssuedAt: issuedAt, ExpiresAt: &expiresAt, CSRFToken: csrfToken,
	}
}

func (s *Server) ensureCSRFCookie(context *gin.Context, expiresAt time.Time) string {
	if value, err := context.Cookie(s.config.Security.CSRF.CookieName); err == nil && value != "" {
		return value
	}
	token := s.newCSRFToken()
	s.setCookie(context, s.config.Security.CSRF.CookieName, token, false, expiresAt)
	return token
}

func (s *Server) newCSRFToken() string {
	value := make([]byte, s.config.Security.CSRF.TokenBytes)
	if _, err := rand.Read(value); err != nil {
		panic("secure random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func (s *Server) setCookie(context *gin.Context, name, value string, httpOnly bool, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = -1
	}
	http.SetCookie(context.Writer, &http.Cookie{
		Name: name, Value: value, Path: s.config.Security.Session.CookiePath,
		Domain: s.config.Security.Session.CookieDomain, Expires: expiresAt, MaxAge: maxAge,
		HttpOnly: httpOnly, Secure: s.config.Security.Session.CookieSecure,
		SameSite: cookieSameSite(s.config.Security.Session.CookieSameSite),
	})
}

func (s *Server) setIdempotentCookie(context *gin.Context, name, value string, httpOnly bool, expiresAt time.Time) {
	http.SetCookie(context.Writer, &http.Cookie{
		Name: name, Value: value, Path: s.config.Security.Session.CookiePath,
		Domain: s.config.Security.Session.CookieDomain, Expires: expiresAt,
		HttpOnly: httpOnly, Secure: s.config.Security.Session.CookieSecure,
		SameSite: cookieSameSite(s.config.Security.Session.CookieSameSite),
	})
}

func registrationInputError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "valid email") ||
		strings.Contains(message, "display name") ||
		strings.Contains(message, "password must") ||
		strings.Contains(message, "password is too long")
}

func (s *Server) clearSessionCookies(context *gin.Context) {
	expiredAt := time.Unix(1, 0).UTC()
	s.setCookie(context, s.config.Security.Session.CookieName, "", true, expiredAt)
	s.setCookie(context, s.config.Security.CSRF.CookieName, "", false, expiredAt)
}

func cookieSameSite(value string) http.SameSite {
	switch value {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

func requestHasSessionCredential(context *gin.Context, cookieName string) bool {
	if value, err := context.Cookie(cookieName); err == nil && value != "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(context.GetHeader("Authorization"))), "bearer ")
}

func noStore(context *gin.Context) {
	context.Header("Cache-Control", "no-store")
	context.Header("Pragma", "no-cache")
}
