package transport

import (
	"crypto/rand"
	"encoding/base64"
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
	issued, err := s.services.Auth.SignUp(
		context.Request.Context(), input.Email, input.DisplayName, input.Password,
		context.Request.UserAgent(), context.ClientIP(),
	)
	if err != nil {
		if registrationInputError(err) {
			problem.Write(context, problem.New(http.StatusUnprocessableEntity, "invalid_input", "Input is invalid", "The display name, email, or password does not satisfy the account policy."))
			return
		}
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	s.writeIssuedSession(context, issued, http.StatusCreated)
}

func (s *Server) LoginSession(context *gin.Context) {
	noStore(context)
	var input loginSessionInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	issued, err := s.services.Auth.SignIn(
		context.Request.Context(), input.Email, input.Password,
		context.Request.UserAgent(), context.ClientIP(),
	)
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	s.writeIssuedSession(context, issued, http.StatusOK)
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
	identity, ok := worksmiddleware.GetIdentity(context)
	if !ok {
		problem.WriteError(context, auth.ErrSessionExpired)
		return
	}
	if identity.Transport == "cookie" {
		if rotating, supportsRotation := s.services.Auth.(RotatingAuthService); supportsRotation {
			issued, err := rotating.Rotate(context.Request.Context(), identity.Token, context.Request.UserAgent(), context.ClientIP())
			if err != nil {
				s.clearSessionCookies(context)
				problem.WriteError(context, err)
				return
			}
			s.writeIssuedSession(context, issued, http.StatusOK)
			return
		}
	}
	csrfToken := ""
	if identity.Transport == "cookie" {
		csrfToken = s.ensureCSRFCookie(context, identity.Session.ExpiresAt)
	}
	context.JSON(http.StatusOK, responseForSession(identity.Session, csrfToken, nil))
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

func (s *Server) writeIssuedSession(context *gin.Context, issued auth.IssuedSession, status int) {
	s.setCookie(context, s.config.Security.Session.CookieName, issued.Token, true, issued.ExpiresAt)
	csrfToken := s.newCSRFToken()
	s.setCookie(context, s.config.Security.CSRF.CookieName, csrfToken, false, issued.ExpiresAt)
	now := time.Now().UTC()
	context.JSON(status, responseForSession(issued.Session, csrfToken, &now))
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
