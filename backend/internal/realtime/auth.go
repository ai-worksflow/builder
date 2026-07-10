package realtime

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
)

var ErrUnauthorized = errors.New("websocket authentication required")

type Principal struct {
	ID        string
	SessionID string
}

type AuthenticationRequest struct {
	BearerToken string
	SessionID   string
	CSRFToken   string
}

type Authenticator interface {
	Authenticate(*http.Request, AuthenticationRequest) (Principal, error)
}

type AuthenticatorFunc func(*http.Request, AuthenticationRequest) (Principal, error)

func (f AuthenticatorFunc) Authenticate(request *http.Request, input AuthenticationRequest) (Principal, error) {
	return f(request, input)
}

type AnonymousAuthenticator struct{}

func (AnonymousAuthenticator) Authenticate(*http.Request, AuthenticationRequest) (Principal, error) {
	return Principal{ID: "anonymous:" + uuid.NewString(), SessionID: uuid.NewString()}, nil
}

type RejectAuthenticator struct{}

func (RejectAuthenticator) Authenticate(*http.Request, AuthenticationRequest) (Principal, error) {
	return Principal{}, ErrUnauthorized
}

type SubscriptionRequest struct {
	ID         string
	Topic      string
	ProjectID  string
	ArtifactID string
	RunID      string
	Cursor     string
}

type SubscriptionAuthorizer interface {
	AuthorizeSubscription(*http.Request, Principal, SubscriptionRequest) error
}

type SubscriptionAuthorizerFunc func(*http.Request, Principal, SubscriptionRequest) error

func (f SubscriptionAuthorizerFunc) AuthorizeSubscription(request *http.Request, principal Principal, subscription SubscriptionRequest) error {
	return f(request, principal, subscription)
}

type AllowAllSubscriptions struct{}

func (AllowAllSubscriptions) AuthorizeSubscription(*http.Request, Principal, SubscriptionRequest) error {
	return nil
}
