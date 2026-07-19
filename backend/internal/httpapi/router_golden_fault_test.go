package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/goldenfault"
	"github.com/worksflow/builder/backend/internal/health"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
)

func TestGoldenFaultRouteIsAbsentUnlessExplicitlyConfigured(t *testing.T) {
	cfg := config.Config{Environment: config.EnvironmentTest, ServiceName: "golden-fault-router-test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := "/v1/qualification/golden-fault-authorities/00000000-0000-4000-8000-000000000001/consume"

	without, err := NewRouter(cfg, logger, RouterOptions{Readiness: health.NewReadiness(time.Second, nil)})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	without.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unconfigured status = %d, body = %s", response.Code, response.Body.String())
	}

	handler, err := transport.NewGoldenFaultHandler(transport.GoldenFaultDependencies{
		Authenticator: routerGoldenFaultAuthenticator{}, Consumer: routerGoldenFaultConsumer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := NewRouter(cfg, logger, RouterOptions{
		Readiness: health.NewReadiness(time.Second, nil), GoldenFault: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	configured.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("configured status = %d, body = %s", response.Code, response.Body.String())
	}
}

type routerGoldenFaultAuthenticator struct{}

func (routerGoldenFaultAuthenticator) AuthenticateGoldenFaultCredential(
	context.Context,
	string,
) (goldenfault.RunPrincipal, error) {
	return goldenfault.RunPrincipal{}, nil
}

type routerGoldenFaultConsumer struct{}

func (routerGoldenFaultConsumer) Consume(
	context.Context,
	goldenfault.RunPrincipal,
	goldenfault.ConsumeCommand,
) (goldenfault.ConsumeRecord, error) {
	return goldenfault.ConsumeRecord{}, goldenfault.ErrFaultCredentialForbidden
}
