package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/health"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
	"github.com/worksflow/builder/backend/internal/lsp"
)

type lspTicketRouterIssuer struct{}

func (lspTicketRouterIssuer) Issue(context.Context, lsp.IssueTicketInput) (lsp.TicketView, error) {
	return lsp.TicketView{}, nil
}

func TestLSPWebSocketRouterRegistersDedicatedUnwrappedUpgradePath(t *testing.T) {
	cfg := config.Config{Environment: config.EnvironmentTest, ServiceName: "lsp-websocket-router-test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	called := false
	router, err := NewRouter(cfg, logger, RouterOptions{
		Readiness: health.NewReadiness(time.Second, nil),
		LSPWebSocket: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			called = request.URL.Path == lsp.TicketWebSocketPath
			writer.WriteHeader(http.StatusTeapot)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, lsp.TicketWebSocketPath+"?ticket=redacted-by-handler", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if !called || response.Code != http.StatusTeapot {
		t.Fatalf("dedicated LSP WebSocket route = called %t status %d", called, response.Code)
	}
	for _, route := range router.Routes() {
		if route.Method == http.MethodGet && route.Path == lsp.TicketWebSocketPath {
			return
		}
	}
	t.Fatal("dedicated LSP WebSocket route was not registered")
}

type lspTicketRouterProjects struct{}

func (lspTicketRouterProjects) ResolveProject(context.Context, string, string) (string, error) {
	return "", nil
}

type lspTicketRouterAuthenticator struct{}

func (lspTicketRouterAuthenticator) Authenticate(context.Context, string) (auth.Session, error) {
	return auth.Session{}, nil
}

func newLSPTicketRouterHandler(t *testing.T) *transport.LSPTicketHandler {
	t.Helper()
	handler, err := transport.NewLSPTicketHandler(transport.LSPTicketDependencies{
		Tickets:  lspTicketRouterIssuer{},
		Projects: lspTicketRouterProjects{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestLSPTicketRouterRegistersProtectedRouteWithoutIdempotency(t *testing.T) {
	cfg := config.Config{Environment: config.EnvironmentTest, ServiceName: "lsp-ticket-router-test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router, err := NewRouter(cfg, logger, RouterOptions{
		Readiness:      health.NewReadiness(time.Second, nil),
		Transport:      transport.NewServer(transport.Services{}, cfg, logger),
		Authentication: lspTicketRouterAuthenticator{},
		LSPTickets:     newLSPTicketRouterHandler(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range router.Routes() {
		if route.Method == "POST" && route.Path == "/v1/sandbox-sessions/:sessionId/lsp-tickets" {
			return
		}
	}
	t.Fatal("LSP ticket route was not registered")
}

func TestLSPTicketRouterRequiresProtectedAPI(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := NewRouter(config.Config{Environment: config.EnvironmentTest}, logger, RouterOptions{
		Readiness:  health.NewReadiness(time.Second, nil),
		LSPTickets: newLSPTicketRouterHandler(t),
	})
	if err == nil || !strings.Contains(err.Error(), "LSP ticket routes require API transport and authentication") {
		t.Fatalf("NewRouter() error = %v", err)
	}
}
