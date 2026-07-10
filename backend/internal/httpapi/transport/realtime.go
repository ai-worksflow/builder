package transport

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/realtime"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type RealtimeAuthenticator struct {
	service AuthService
	config  config.SecurityConfig
}

func NewRealtimeAuthenticator(service AuthService, cfg config.SecurityConfig) *RealtimeAuthenticator {
	return &RealtimeAuthenticator{service: service, config: cfg}
}

func (a *RealtimeAuthenticator) Authenticate(request *http.Request, input realtime.AuthenticationRequest) (realtime.Principal, error) {
	if a.service == nil {
		return realtime.Principal{}, realtime.ErrUnauthorized
	}
	token := strings.TrimSpace(input.BearerToken)
	cookieTransport := false
	if token == "" {
		if authorization := strings.TrimSpace(request.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
			token = strings.TrimSpace(authorization[len("Bearer "):])
		}
	}
	if token == "" {
		cookie, err := request.Cookie(a.config.Session.CookieName)
		if err != nil || cookie.Value == "" {
			return realtime.Principal{}, realtime.ErrUnauthorized
		}
		token = cookie.Value
		cookieTransport = true
	}
	if cookieTransport {
		csrfCookie, err := request.Cookie(a.config.CSRF.CookieName)
		if err != nil || input.CSRFToken == "" || subtle.ConstantTimeCompare([]byte(csrfCookie.Value), []byte(input.CSRFToken)) != 1 {
			return realtime.Principal{}, realtime.ErrUnauthorized
		}
	}
	session, err := a.service.Authenticate(request.Context(), token)
	if err != nil {
		return realtime.Principal{}, err
	}
	if input.SessionID != "" && input.SessionID != session.ID {
		return realtime.Principal{}, realtime.ErrUnauthorized
	}
	return realtime.Principal{ID: session.User.ID, SessionID: session.ID}, nil
}

type RealtimeSubscriptionAuthorizer struct {
	access   AccessControl
	database *gorm.DB
}

func NewRealtimeSubscriptionAuthorizer(access AccessControl, database *gorm.DB) *RealtimeSubscriptionAuthorizer {
	return &RealtimeSubscriptionAuthorizer{access: access, database: database}
}

func (a *RealtimeSubscriptionAuthorizer) AuthorizeSubscription(request *http.Request, principal realtime.Principal, subscription realtime.SubscriptionRequest) error {
	if a.access == nil || a.database == nil {
		return realtime.ErrUnauthorized
	}
	if _, err := a.access.Authorize(request.Context(), subscription.ProjectID, principal.ID, core.ActionView); err != nil {
		return err
	}
	projectID, err := uuid.Parse(subscription.ProjectID)
	if err != nil {
		return core.ErrInvalidInput
	}
	switch subscription.Topic {
	case "project":
		return nil
	case "artifact":
		artifactID, err := uuid.Parse(subscription.ArtifactID)
		if err != nil {
			return core.ErrInvalidInput
		}
		var count int64
		err = a.database.WithContext(request.Context()).Model(&storage.ArtifactModel{}).
			Where("id = ? AND project_id = ?", artifactID, projectID).Count(&count).Error
		if err != nil {
			return err
		}
		if count != 1 {
			return core.ErrNotFound
		}
		return nil
	case "run":
		runID, err := uuid.Parse(subscription.RunID)
		if err != nil {
			return core.ErrInvalidInput
		}
		var count int64
		err = a.database.WithContext(request.Context()).Model(&storage.WorkflowRunModel{}).
			Where("id = ? AND project_id = ?", runID, projectID).Count(&count).Error
		if err != nil {
			return err
		}
		if count != 1 {
			return core.ErrNotFound
		}
		return nil
	default:
		return errors.New("unsupported subscription topic")
	}
}
