package httpapi

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/health"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
)

type constructorRouterAPI struct{}

func (constructorRouterAPI) CompileForManifest(context.Context, string, string, constructor.CompileForManifestInput) (constructor.ApplicationBuildContract, error) {
	return constructor.ApplicationBuildContract{}, nil
}

func (constructorRouterAPI) GetForManifest(context.Context, string, string) (constructor.ApplicationBuildContract, error) {
	return constructor.ApplicationBuildContract{}, nil
}

func (constructorRouterAPI) Get(context.Context, string, string) (constructor.ApplicationBuildContract, error) {
	return constructor.ApplicationBuildContract{}, nil
}

type constructorRouterAuthenticator struct{}

func (constructorRouterAuthenticator) Authenticate(context.Context, string) (auth.Session, error) {
	return auth.Session{}, nil
}

func TestConstructorRouterOptionsRegisterRoutes(t *testing.T) {
	cfg := config.Config{Environment: config.EnvironmentTest, ServiceName: "constructor-router-test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := transport.NewConstructorHandler(transport.ConstructorDependencies{Service: constructorRouterAPI{}})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(cfg, logger, RouterOptions{
		Readiness:      health.NewReadiness(time.Second, nil),
		Transport:      transport.NewServer(transport.Services{}, cfg, logger),
		Authentication: constructorRouterAuthenticator{},
		Idempotency:    &worksmiddleware.IdempotencyRepository{},
		Constructor:    handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"POST /v1/build-manifests/:bundleId/build-contracts": false,
		"GET /v1/build-manifests/:bundleId/build-contract":   false,
		"GET /v1/application-build-contracts/:contractId":    false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("constructor route %s was not registered", route)
		}
	}
}

func TestConstructorRouterOptionsRequireDurableIdempotency(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := transport.NewConstructorHandler(transport.ConstructorDependencies{Service: constructorRouterAPI{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRouter(config.Config{Environment: config.EnvironmentTest}, logger, RouterOptions{
		Readiness:   health.NewReadiness(time.Second, nil),
		Constructor: handler,
	})
	if err == nil || !strings.Contains(err.Error(), "constructor routes require durable idempotency") {
		t.Fatalf("NewRouter() error = %v", err)
	}
}
