package transport_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/worksflow/builder/backend/internal/httpapi/transport"
	"github.com/worksflow/builder/backend/internal/realtime"
)

func TestRealtimeAuthenticatorUsesCookieSessionAndCSRF(t *testing.T) {
	service := &fakeAuthService{}
	cfg := testConfig().Security
	authenticator := transport.NewRealtimeAuthenticator(service, cfg)
	request := httptest.NewRequest("GET", "/v1/ws", nil)
	request.AddCookie(testCookie(testSessionCookie, "valid-token"))
	request.AddCookie(testCookie(testCSRFCookie, "csrf-token"))
	principal, err := authenticator.Authenticate(request, realtime.AuthenticationRequest{CSRFToken: "csrf-token"})
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.ID != testUserID || principal.SessionID == "" {
		t.Fatalf("principal = %#v", principal)
	}
	if _, err := authenticator.Authenticate(request, realtime.AuthenticationRequest{CSRFToken: "wrong"}); err == nil {
		t.Fatal("cookie authentication accepted the wrong CSRF token")
	}
}

func TestRealtimeAuthenticatorAllowsBearerWithoutCSRF(t *testing.T) {
	authenticator := transport.NewRealtimeAuthenticator(&fakeAuthService{}, testConfig().Security)
	principal, err := authenticator.Authenticate(
		httptest.NewRequest("GET", "/v1/ws", nil),
		realtime.AuthenticationRequest{BearerToken: "valid-token"},
	)
	if err != nil || principal.ID != testUserID {
		t.Fatalf("principal = %#v, error = %v", principal, err)
	}
}

func testCookie(name, value string) *http.Cookie {
	return &http.Cookie{Name: name, Value: value}
}
