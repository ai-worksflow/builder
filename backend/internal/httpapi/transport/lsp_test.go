package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/lsp"
)

type lspTicketIssuerFake struct {
	input lsp.IssueTicketInput
	view  lsp.TicketView
	err   error
	calls int
}

func (fake *lspTicketIssuerFake) Issue(
	_ context.Context,
	input lsp.IssueTicketInput,
) (lsp.TicketView, error) {
	fake.calls++
	fake.input = input
	return fake.view, fake.err
}

type lspProjectResolverFake struct {
	projectID string
	err       error
	actorID   string
	sessionID string
	calls     int
}

func (fake *lspProjectResolverFake) ResolveProject(
	_ context.Context,
	sessionID, actorID string,
) (string, error) {
	fake.calls++
	fake.sessionID, fake.actorID = sessionID, actorID
	return fake.projectID, fake.err
}

func lspTransportHead() lsp.SandboxHeadFence {
	return lsp.SandboxHeadFence{
		ProjectID: sandboxTransportProject, SessionID: sandboxTransportSession, SessionEpoch: 3,
		CandidateID: sandboxTransportCandidate, Version: 8, JournalSequence: 2,
		WriterLeaseEpoch: 1,
		TreeHash:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func lspTicketTransportRouter(
	t *testing.T,
	issuer *lspTicketIssuerFake,
	resolver *lspProjectResolverFake,
	middleware ...gin.HandlerFunc,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session: auth.Session{User: auth.User{ID: sandboxTransportActor}}, Transport: "cookie",
		})
		c.Next()
	})
	handler, err := NewLSPTicketHandler(LSPTicketDependencies{Tickets: issuer, Projects: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterLSPTicketRoutes(router, handler, middleware...); err != nil {
		t.Fatal(err)
	}
	return router
}

func TestLSPTicketRouteUsesAuthenticatedIdentityStrictScopeAndNoStore(t *testing.T) {
	issuer := &lspTicketIssuerFake{view: lsp.TicketView{
		SchemaVersion: lsp.TicketSchemaVersion,
		ID:            "10000000-0000-4000-8000-000000000099", Ticket: strings.Repeat("A", 43),
		WebSocketPath: lsp.TicketWebSocketPath, Subprotocol: lsp.TicketSubprotocol,
	}}
	resolver := &lspProjectResolverFake{projectID: sandboxTransportProject}
	middlewareCalls := 0
	router := lspTicketTransportRouter(t, issuer, resolver, func(c *gin.Context) {
		middlewareCalls++
		c.Next()
	})
	requestBody := lsp.TicketRequest{
		SchemaVersion: lsp.TicketRequestSchemaVersion, Mode: lsp.TicketModeEditor,
		Head: lspTransportHead(), TemplateRelease: lsp.ExactTemplateRelease{
			ID:          "10000000-0000-4000-8000-000000000088",
			ContentHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		ProfileIDs: []string{"typescript"},
	}
	encoded, _ := json.Marshal(requestBody)
	request := httptest.NewRequest(
		http.MethodPost, "/sandbox-sessions/"+sandboxTransportSession+"/lsp-tickets", bytes.NewReader(encoded),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://builder.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || middlewareCalls != 1 || issuer.calls != 1 || resolver.calls != 1 ||
		response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("Location") != "/v1/lsp-tickets/"+issuer.view.ID {
		t.Fatalf("ticket response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if resolver.actorID != sandboxTransportActor || resolver.sessionID != sandboxTransportSession ||
		issuer.input.ProjectID != sandboxTransportProject || issuer.input.SessionID != sandboxTransportSession ||
		issuer.input.ActorID != sandboxTransportActor || issuer.input.Origin != "https://builder.example" ||
		issuer.input.Mode != lsp.TicketModeEditor || issuer.input.Head != requestBody.Head ||
		issuer.input.TemplateRelease != requestBody.TemplateRelease ||
		len(issuer.input.ProfileIDs) != 1 || issuer.input.ProfileIDs[0] != "typescript" {
		t.Fatalf("ticket authority input drifted: resolver=%#v input=%#v", resolver, issuer.input)
	}
}

func TestLSPTicketRouteRejectsDuplicateOrUnknownFieldsBeforeAuthority(t *testing.T) {
	issuer := &lspTicketIssuerFake{}
	resolver := &lspProjectResolverFake{projectID: sandboxTransportProject}
	router := lspTicketTransportRouter(t, issuer, resolver)
	for _, body := range []string{
		`{"schemaVersion":"sandbox-lsp-ticket-request/v1","schemaVersion":"sandbox-lsp-ticket-request/v1"}`,
		`{"unknown":true}`,
	} {
		request := httptest.NewRequest(
			http.MethodPost, "/sandbox-sessions/"+sandboxTransportSession+"/lsp-tickets", strings.NewReader(body),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "lsp_message_malformed") {
			t.Fatalf("malformed ticket response=%d body=%s", response.Code, response.Body.String())
		}
	}
	if issuer.calls != 0 || resolver.calls != 0 {
		t.Fatalf("malformed DTO reached authority: issuer=%d resolver=%d", issuer.calls, resolver.calls)
	}
}

func TestLSPTicketRouteMapsStableGateErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"origin", lsp.ErrOriginForbidden, http.StatusForbidden, "lsp_origin_forbidden"},
		{"forbidden", lsp.ErrForbidden, http.StatusForbidden, "lsp_forbidden"},
		{"session", lsp.ErrSessionNotReady, http.StatusConflict, "lsp_session_not_ready"},
		{"head", lsp.ErrHeadStale, http.StatusConflict, "lsp_head_stale"},
		{"profile", lsp.ErrProfileNotDeclared, http.StatusUnprocessableEntity, "lsp_profile_not_declared"},
		{"store", lsp.ErrTicketUnavailable, http.StatusServiceUnavailable, "lsp_ticket_store_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			issuer := &lspTicketIssuerFake{err: test.err}
			resolver := &lspProjectResolverFake{projectID: sandboxTransportProject}
			router := lspTicketTransportRouter(t, issuer, resolver)
			body, _ := json.Marshal(lsp.TicketRequest{
				SchemaVersion: lsp.TicketRequestSchemaVersion, Mode: lsp.TicketModeSnapshot,
				Head: lspTransportHead(), TemplateRelease: lsp.ExactTemplateRelease{
					ID:          "10000000-0000-4000-8000-000000000088",
					ContentHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				},
				ProfileIDs: []string{"typescript"},
			})
			request := httptest.NewRequest(
				http.MethodPost, "/sandbox-sessions/"+sandboxTransportSession+"/lsp-tickets", bytes.NewReader(body),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "https://builder.example")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) ||
				strings.Contains(response.Body.String(), test.err.Error()) {
				t.Fatalf("gate response=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestNewLSPTicketHandlerFailsClosedWithoutDependencies(t *testing.T) {
	if _, err := NewLSPTicketHandler(LSPTicketDependencies{}); err == nil {
		t.Fatal("nil LSP ticket dependencies were accepted")
	}
	if err := RegisterLSPTicketRoutes(nil, &LSPTicketHandler{}); err == nil {
		t.Fatal("nil LSP routes were accepted")
	}
}
