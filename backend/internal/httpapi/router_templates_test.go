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
	"github.com/worksflow/builder/backend/internal/health"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
	"github.com/worksflow/builder/backend/internal/templates"
)

type templateRegistryRouterAPI struct{}

func (templateRegistryRouterAPI) ListTemplateReleases(context.Context, templates.TemplateReleaseListOptions) ([]templates.TemplateReleaseRegistration, error) {
	return nil, nil
}

func (templateRegistryRouterAPI) GetTemplateRelease(context.Context, string) (templates.TemplateReleaseRegistration, error) {
	return templates.TemplateReleaseRegistration{}, nil
}

func (templateRegistryRouterAPI) GetTemplateReleaseExact(context.Context, templates.TemplateReleaseRef) (templates.TemplateReleaseRegistration, error) {
	return templates.TemplateReleaseRegistration{}, nil
}

func (templateRegistryRouterAPI) ListFullStackTemplates(context.Context, templates.FullStackTemplateListOptions) ([]templates.FullStackTemplateRegistration, error) {
	return nil, nil
}

func (templateRegistryRouterAPI) GetFullStackTemplate(context.Context, string) (templates.FullStackTemplateRegistration, error) {
	return templates.FullStackTemplateRegistration{}, nil
}

func (templateRegistryRouterAPI) GetFullStackTemplateExact(context.Context, templates.ExactFullStackTemplateRef) (templates.FullStackTemplateRegistration, error) {
	return templates.FullStackTemplateRegistration{}, nil
}

func (templateRegistryRouterAPI) ResolveForNewBuild(context.Context, templates.ExactFullStackTemplateRef) (templates.ResolvedFullStackTemplate, error) {
	return templates.ResolvedFullStackTemplate{}, nil
}

type templateRegistryRouterAuthenticator struct{}

func (templateRegistryRouterAuthenticator) Authenticate(context.Context, string) (auth.Session, error) {
	return auth.Session{}, nil
}

func TestTemplateRegistryRouterOptionsRegisterProtectedReadRoutesWithoutIdempotency(t *testing.T) {
	cfg := config.Config{Environment: config.EnvironmentTest, ServiceName: "template-registry-router-test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := transport.NewTemplateRegistryHandler(transport.TemplateRegistryDependencies{Registry: templateRegistryRouterAPI{}})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(cfg, logger, RouterOptions{
		Readiness:        health.NewReadiness(time.Second, nil),
		Transport:        transport.NewServer(transport.Services{}, cfg, logger),
		Authentication:   templateRegistryRouterAuthenticator{},
		TemplateRegistry: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"GET /v1/template-releases":                false,
		"GET /v1/template-releases/:releaseId":     false,
		"GET /v1/full-stack-templates":             false,
		"GET /v1/full-stack-templates/:templateId": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("template registry route %s was not registered", route)
		}
	}
}

func TestTemplateRegistryRouterOptionsRequireProtectedAPI(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := transport.NewTemplateRegistryHandler(transport.TemplateRegistryDependencies{Registry: templateRegistryRouterAPI{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRouter(config.Config{Environment: config.EnvironmentTest}, logger, RouterOptions{
		Readiness:        health.NewReadiness(time.Second, nil),
		TemplateRegistry: handler,
	})
	if err == nil || !strings.Contains(err.Error(), "template registry routes require API transport and authentication") {
		t.Fatalf("NewRouter() error = %v", err)
	}
}
