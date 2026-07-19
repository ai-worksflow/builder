package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/lsp"
	"github.com/worksflow/builder/backend/internal/templates"
)

const (
	lspWebSocketProject   = "20000000-0000-4000-8000-000000000001"
	lspWebSocketSession   = "20000000-0000-4000-8000-000000000002"
	lspWebSocketCandidate = "20000000-0000-4000-8000-000000000003"
	lspWebSocketActor     = "20000000-0000-4000-8000-000000000004"
	lspWebSocketRelease   = "20000000-0000-4000-8000-000000000005"
	lspWebSocketTicketID  = "20000000-0000-4000-8000-000000000006"
	lspWebSocketOpenID    = "20000000-0000-4000-8000-000000000007"
)

var lspWebSocketSecret = strings.Repeat("A", 43)

type lspWebSocketTicketsFake struct {
	mu       sync.Mutex
	grant    lsp.TicketGrant
	outcomes []error
	calls    int
	secrets  []string
	origins  []string
}

func (fake *lspWebSocketTicketsFake) Consume(
	_ context.Context,
	secret, origin string,
) (lsp.TicketGrant, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	index := fake.calls
	fake.calls++
	fake.secrets = append(fake.secrets, secret)
	fake.origins = append(fake.origins, origin)
	if index < len(fake.outcomes) && fake.outcomes[index] != nil {
		return lsp.TicketGrant{}, fake.outcomes[index]
	}
	return fake.grant, nil
}

func (fake *lspWebSocketTicketsFake) callCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.calls
}

type lspGatewayCall struct {
	grant lsp.TicketGrant
	bind  lsp.ClientBind
}

type lspBindingGatewayFake struct {
	calls chan lspGatewayCall
	err   error
}

func (fake *lspBindingGatewayFake) ServeBoundLSP(
	_ context.Context,
	_ *websocket.Conn,
	grant lsp.TicketGrant,
	bind lsp.ClientBind,
) error {
	fake.calls <- lspGatewayCall{grant: grant, bind: bind}
	return fake.err
}

func lspWebSocketDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func lspWebSocketLimits() templates.LanguageServerLimits {
	return templates.LanguageServerLimits{
		StartupTimeoutMillis: 10_000, RequestTimeoutMillis: 5_000, ShutdownTimeoutMillis: 2_000,
		CPUMillis: 1_000, MemoryBytes: 512 << 20, PIDLimit: 64, TempBytes: 256 << 20,
		CacheBytes: 256 << 20, MaxOpenDocuments: 4, MaxDocumentBytes: 1 << 20,
		MaxTotalSyncBytes: 8 << 20, MaxFrameBytes: 512 << 10, MaxResultBytes: 1 << 20,
		MaxConcurrentRequests: 8, RequestsPerSecond: 10, RequestBurst: 20,
		MaxDiagnosticsPerDocument: 1_000, MaxCompletionItems: 200, MaxNavigationLocations: 2_000,
	}
}

func lspWebSocketProfile(t *testing.T) lsp.ProfileIdentity {
	t.Helper()
	profile := templates.LanguageServerProfile{
		SchemaVersion: templates.LanguageServerProfileSchemaVersion,
		ID:            "typescript", ServiceID: "web", LanguageIDs: []string{"typescript"}, FileGlobs: []string{"**/*.ts"},
		ProtocolVersion: "3.17",
		Runtime: templates.LanguageServerRuntime{
			Image:          "ghcr.io/worksflow/lsp@" + lspWebSocketDigest("3"),
			ExecutablePath: "/opt/lsp/typescript-language-server", ExecutableDigest: lspWebSocketDigest("4"),
			Argv:                   []string{"/opt/lsp/typescript-language-server", "--stdio"},
			WorkingDirectoryPolicy: "service-root",
		},
		ServerInfo:                   templates.LanguageServerInfo{Name: "typescript-language-server", Version: "4.3.3"},
		InitializationParametersHash: templates.ProductionV1InitializationParametersHash(),
		WorkspaceConfigurationHash:   templates.ProductionV1WorkspaceConfigurationHash(),
		RequireVersionedDiagnostics:  true,
		Methods:                      []string{"textDocument/hover"},
		Limits:                       lspWebSocketLimits(),
		Isolation: templates.LanguageServerIsolation{
			NetworkPolicy: "none", WorkspaceMountPolicy: "read-only",
			TempPolicy: "isolated-bounded", CachePolicy: "isolated-bounded",
			WorkspacePluginPolicy: "forbidden", DynamicSDKPolicy: "forbidden",
			DynamicRegistrationPolicy: "forbidden", ConfigurationCommandPolicy: "forbidden",
			PackageManagerHookPolicy: "forbidden",
		},
	}
	var err error
	profile.CapabilityHash, err = templates.ComputeLanguageServerCapabilityHash(profile.Methods)
	if err != nil {
		t.Fatal(err)
	}
	profile.ContentHash, err = templates.ComputeLanguageServerProfileContentHash(profile)
	if err != nil {
		t.Fatal(err)
	}
	return lsp.ProfileIdentity{
		LanguageServerProfile: profile,
		TemplateRelease: lsp.ExactTemplateRelease{
			ID: lspWebSocketRelease, ContentHash: lspWebSocketDigest("2"),
		},
		EffectiveLimits: profile.Limits,
	}
}

func lspWebSocketGrant(t *testing.T) lsp.TicketGrant {
	t.Helper()
	now := time.Now().UTC()
	profile := lspWebSocketProfile(t)
	return lsp.TicketGrant{
		SchemaVersion: lsp.TicketSchemaVersion, ID: lspWebSocketTicketID,
		ProjectID: lspWebSocketProject, SessionID: lspWebSocketSession, ActorID: lspWebSocketActor,
		Origin: "https://builder.example", Mode: lsp.TicketModeEditor,
		Head: lsp.SandboxHeadFence{
			ProjectID: lspWebSocketProject, SessionID: lspWebSocketSession, SessionEpoch: 3,
			CandidateID: lspWebSocketCandidate, Version: 8, JournalSequence: 2,
			WriterLeaseEpoch: 1, TreeHash: lspWebSocketDigest("a"),
		},
		TemplateRelease: profile.TemplateRelease, Profiles: []lsp.ProfileIdentity{profile},
		IssuedAt: now, ExpiresAt: now.Add(20 * time.Second),
	}
}

func newLSPWebSocketTestServer(
	t *testing.T,
	tickets *lspWebSocketTicketsFake,
	gateway *lspBindingGatewayFake,
	config LSPWebSocketConfig,
) *httptest.Server {
	t.Helper()
	handler, err := NewLSPWebSocketHandler(LSPWebSocketDependencies{
		Tickets: tickets, Gateway: gateway, Config: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(handler)
}

func dialLSPWebSocket(
	server *httptest.Server,
	origin string,
	protocols []string,
	query string,
	headers http.Header,
) (*websocket.Conn, *http.Response, error) {
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Origin", origin)
	dialer := websocket.Dialer{Subprotocols: protocols, HandshakeTimeout: time.Second}
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + lsp.TicketWebSocketPath + query
	return dialer.Dial(endpoint, headers)
}

func validLSPClientBind(t *testing.T, grant lsp.TicketGrant, connectionID string) lsp.ClientBind {
	t.Helper()
	modelURI, err := lsp.CandidateModelURI(grant.ProjectID, grant.Head.CandidateID, "apps/web/page.ts")
	if err != nil {
		t.Fatal(err)
	}
	return lsp.ClientBind{
		SchemaVersion: lsp.BindingSchemaVersion, Kind: "client.bind", ConnectionID: connectionID,
		BindingID: nil, Sequence: 1, Head: grant.Head, Profile: grant.Profiles[0],
		Documents: []lsp.DocumentFence{{
			ModelURI: modelURI, OpenID: lspWebSocketOpenID, ModelVersion: 1,
			SavedContentHash: lspWebSocketDigest("b"),
		}},
	}
}

func TestLSPWebSocketPreflightRejectionNeverConsumesTicket(t *testing.T) {
	for _, test := range []struct {
		name      string
		origin    string
		protocols []string
		query     string
		config    LSPWebSocketConfig
		status    int
		code      string
	}{
		{
			name: "origin", origin: "https://attacker.example", protocols: []string{lsp.TicketSubprotocol},
			query:  "?ticket=" + lspWebSocketSecret,
			config: LSPWebSocketConfig{AllowedOrigins: []string{"https://builder.example"}},
			status: http.StatusForbidden, code: "lsp_origin_forbidden",
		},
		{
			name: "missing subprotocol", origin: "https://builder.example",
			query:  "?ticket=" + lspWebSocketSecret,
			config: LSPWebSocketConfig{AllowedOrigins: []string{"https://builder.example"}},
			status: http.StatusBadRequest, code: "lsp_subprotocol_required",
		},
		{
			name: "ambiguous subprotocol", origin: "https://builder.example",
			protocols: []string{lsp.TicketSubprotocol, "other.v1"}, query: "?ticket=" + lspWebSocketSecret,
			config: LSPWebSocketConfig{AllowedOrigins: []string{"https://builder.example"}},
			status: http.StatusBadRequest, code: "lsp_subprotocol_required",
		},
		{
			name: "TLS", origin: "https://builder.example", protocols: []string{lsp.TicketSubprotocol},
			query:  "?ticket=" + lspWebSocketSecret,
			config: LSPWebSocketConfig{AllowedOrigins: []string{"https://builder.example"}, RequireTLS: true},
			status: http.StatusForbidden, code: "lsp_forbidden",
		},
		{
			name: "duplicate ticket", origin: "https://builder.example", protocols: []string{lsp.TicketSubprotocol},
			query:  "?ticket=" + lspWebSocketSecret + "&ticket=" + lspWebSocketSecret,
			config: LSPWebSocketConfig{AllowedOrigins: []string{"https://builder.example"}},
			status: http.StatusUnauthorized, code: "lsp_ticket_required",
		},
		{
			name: "trailing query separator", origin: "https://builder.example", protocols: []string{lsp.TicketSubprotocol},
			query:  "?ticket=" + lspWebSocketSecret + "&",
			config: LSPWebSocketConfig{AllowedOrigins: []string{"https://builder.example"}},
			status: http.StatusUnauthorized, code: "lsp_ticket_required",
		},
		{
			name: "encoded ticket alias", origin: "https://builder.example", protocols: []string{lsp.TicketSubprotocol},
			query:  "?ticket=%41" + strings.TrimPrefix(lspWebSocketSecret, "A"),
			config: LSPWebSocketConfig{AllowedOrigins: []string{"https://builder.example"}},
			status: http.StatusUnauthorized, code: "lsp_ticket_required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tickets := &lspWebSocketTicketsFake{grant: lspWebSocketGrant(t)}
			gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
			server := newLSPWebSocketTestServer(t, tickets, gateway, test.config)
			defer server.Close()
			connection, response, err := dialLSPWebSocket(server, test.origin, test.protocols, test.query, nil)
			if connection != nil {
				connection.Close()
				t.Fatal("preflight rejection upgraded the connection")
			}
			if err == nil || response == nil || response.StatusCode != test.status {
				t.Fatalf("preflight response=%v err=%v", response, err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if !strings.Contains(string(body), test.code) || strings.Contains(string(body), lspWebSocketSecret) {
				t.Fatalf("problem body leaked or drifted: %s", body)
			}
			if tickets.callCount() != 0 {
				t.Fatalf("preflight rejection consumed %d tickets", tickets.callCount())
			}
		})
	}
}

func TestLSPWebSocketMalformedUpgradeDoesNotConsumeTicket(t *testing.T) {
	tickets := &lspWebSocketTicketsFake{grant: lspWebSocketGrant(t)}
	gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
	handler, err := NewLSPWebSocketHandler(LSPWebSocketDependencies{
		Tickets: tickets, Gateway: gateway,
		Config: LSPWebSocketConfig{AllowedOrigins: []string{"https://builder.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, lsp.TicketWebSocketPath+"?ticket="+lspWebSocketSecret, nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "not-a-canonical-key")
	request.Header.Set("Sec-WebSocket-Protocol", lsp.TicketSubprotocol)
	request.Header.Set("Origin", "https://builder.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "lsp_message_malformed") ||
		tickets.callCount() != 0 {
		t.Fatalf("malformed upgrade response=%d body=%s consumes=%d", response.Code, response.Body.String(), tickets.callCount())
	}
	if request.URL.RawQuery != "" || strings.Contains(request.RequestURI, lspWebSocketSecret) {
		t.Fatalf("ticket query remained observable: URL=%q requestURI=%q", request.URL.String(), request.RequestURI)
	}
}

func TestLSPWebSocketHeadDriftBurnsTicketAndReplayCannotConnect(t *testing.T) {
	tickets := &lspWebSocketTicketsFake{
		grant: lspWebSocketGrant(t), outcomes: []error{lsp.ErrHeadStale, lsp.ErrTicketConsumed},
	}
	gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
	server := newLSPWebSocketTestServer(t, tickets, gateway, LSPWebSocketConfig{
		AllowedOrigins: []string{"https://builder.example"},
	})
	defer server.Close()
	for index, want := range []struct {
		status int
		code   string
	}{{http.StatusConflict, "lsp_head_stale"}, {http.StatusUnauthorized, "lsp_ticket_rejected"}} {
		connection, response, err := dialLSPWebSocket(
			server, "https://builder.example", []string{lsp.TicketSubprotocol},
			"?ticket="+lspWebSocketSecret, nil,
		)
		if connection != nil {
			connection.Close()
			t.Fatalf("attempt %d unexpectedly connected", index)
		}
		if err == nil || response == nil || response.StatusCode != want.status {
			t.Fatalf("attempt %d response=%v err=%v", index, response, err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if !strings.Contains(string(body), want.code) || strings.Contains(string(body), lspWebSocketSecret) {
			t.Fatalf("attempt %d body=%s", index, body)
		}
	}
	if tickets.callCount() != 2 {
		t.Fatalf("consume calls=%d", tickets.callCount())
	}
	select {
	case <-gateway.calls:
		t.Fatal("head-drift ticket reached the bound gateway")
	default:
	}
}

func TestLSPWebSocketDelayedAndMalformedBindNeverReachGateway(t *testing.T) {
	for _, test := range []struct {
		name        string
		closeCode   int
		closeReason string
		send        func(*testing.T, *websocket.Conn, lsp.ConnectionHello, lsp.TicketGrant)
	}{
		{name: "delayed", closeCode: lspCloseMalformed, closeReason: "lsp_message_malformed"},
		{name: "malformed", closeCode: lspCloseMalformed, closeReason: "lsp_message_malformed", send: func(t *testing.T, connection *websocket.Conn, _ lsp.ConnectionHello, _ lsp.TicketGrant) {
			t.Helper()
			if err := connection.WriteMessage(websocket.TextMessage, []byte(`{"kind":"client.bind"}`)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "binary", closeCode: lspCloseMalformed, closeReason: "lsp_message_malformed", send: func(t *testing.T, connection *websocket.Conn, _ lsp.ConnectionHello, _ lsp.TicketGrant) {
			t.Helper()
			if err := connection.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stale document", closeCode: lspCloseDocumentStale, closeReason: "lsp_document_stale", send: func(t *testing.T, connection *websocket.Conn, hello lsp.ConnectionHello, grant lsp.TicketGrant) {
			t.Helper()
			bind := validLSPClientBind(t, grant, hello.ConnectionID)
			bind.Documents[0].ModelURI = strings.Replace(
				bind.Documents[0].ModelURI,
				lspWebSocketCandidate,
				"20000000-0000-4000-8000-000000000099",
				1,
			)
			encoded, err := json.Marshal(bind)
			if err != nil {
				t.Fatal(err)
			}
			if err := connection.WriteMessage(websocket.TextMessage, encoded); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			grant := lspWebSocketGrant(t)
			tickets := &lspWebSocketTicketsFake{grant: grant}
			gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
			server := newLSPWebSocketTestServer(t, tickets, gateway, LSPWebSocketConfig{
				AllowedOrigins: []string{"https://builder.example"}, BindTimeout: 80 * time.Millisecond,
				WriteWait: 100 * time.Millisecond,
			})
			defer server.Close()
			connection, _, err := dialLSPWebSocket(
				server, "https://builder.example", []string{lsp.TicketSubprotocol},
				"?ticket="+lspWebSocketSecret, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			var hello lsp.ConnectionHello
			if err := connection.ReadJSON(&hello); err != nil {
				t.Fatal(err)
			}
			if hello.Kind != "server.hello" || hello.TicketID != grant.ID {
				t.Fatalf("hello=%#v", hello)
			}
			if test.send != nil {
				test.send(t, connection, hello, grant)
			}
			_, _, readErr := connection.ReadMessage()
			var closeErr *websocket.CloseError
			if !errors.As(readErr, &closeErr) || closeErr.Code != test.closeCode || closeErr.Text != test.closeReason {
				t.Fatalf("bind close=%v", readErr)
			}
			select {
			case <-gateway.calls:
				t.Fatal("invalid bind reached the gateway")
			default:
			}
		})
	}
}

func TestLSPWebSocketExactBindIsOnlyGatewayEntry(t *testing.T) {
	grant := lspWebSocketGrant(t)
	tickets := &lspWebSocketTicketsFake{grant: grant}
	gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
	server := newLSPWebSocketTestServer(t, tickets, gateway, LSPWebSocketConfig{
		AllowedOrigins: []string{"https://builder.example"}, BindTimeout: time.Second,
	})
	defer server.Close()
	connection, response, err := dialLSPWebSocket(
		server, "https://builder.example", []string{lsp.TicketSubprotocol},
		"?ticket="+lspWebSocketSecret, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if response == nil || connection.Subprotocol() != lsp.TicketSubprotocol {
		t.Fatalf("subprotocol=%q response=%v", connection.Subprotocol(), response)
	}
	var hello lsp.ConnectionHello
	if err := connection.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if hello.SchemaVersion != lsp.ConnectionSchemaVersion || hello.Kind != "server.hello" ||
		hello.TicketID != grant.ID || !hello.Head.Equal(grant.Head) || hello.Sequence != 0 ||
		time.Until(hello.BindDeadlineAt) <= 0 || time.Until(hello.BindDeadlineAt) > time.Second {
		t.Fatalf("server hello drifted: %#v", hello)
	}
	want := validLSPClientBind(t, grant, hello.ConnectionID)
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteMessage(websocket.TextMessage, encoded); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-gateway.calls:
		if got.grant.ID != grant.ID || got.bind.ConnectionID != hello.ConnectionID ||
			!got.bind.Head.Equal(grant.Head) || len(got.bind.Documents) != 1 ||
			!got.bind.Documents[0].Equal(want.Documents[0]) || got.bind.Profile.ContentHash != want.Profile.ContentHash {
			t.Fatalf("gateway call drifted: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("exact client.bind did not reach the gateway")
	}
	if tickets.callCount() != 1 {
		t.Fatalf("ticket consume calls=%d", tickets.callCount())
	}
}

func TestLSPWebSocketGatewayFailureUsesStableTypedClose(t *testing.T) {
	for _, test := range []struct {
		name       string
		gatewayErr error
		closeCode  int
		reason     string
	}{
		{
			name: "runtime unavailable", gatewayErr: errors.New("sensitive runtime failure"),
			closeCode: lspCloseRuntimeUnavailable, reason: "lsp_runtime_unavailable",
		},
		{
			name: "stale authority", gatewayErr: errors.Join(lsp.ErrGatewayStale, errors.New("sensitive head")),
			closeCode: lspCloseDocumentStale, reason: "lsp_head_stale",
		},
		{
			name: "editor conflict", gatewayErr: errors.Join(lsp.ErrGatewayEditorLeaseConflict, errors.New("sensitive owner")),
			closeCode: lspCloseDocumentStale, reason: "lsp_editor_conflict",
		},
		{
			name: "editor lease lost", gatewayErr: errors.Join(lsp.ErrGatewayEditorLeaseLost, errors.New("sensitive owner")),
			closeCode: lspCloseDocumentStale, reason: "lsp_editor_lease_lost",
		},
		{
			name: "lease store unavailable", gatewayErr: errors.Join(lsp.ErrGatewaySecurityUnavailable, errors.New("sensitive Redis")),
			closeCode: lspCloseRuntimeUnavailable, reason: "lsp_security_unavailable",
		},
		{
			name: "runtime capability violation", gatewayErr: errors.Join(lsp.ErrGatewayServerViolation, errors.New("sensitive callback")),
			closeCode: lspCloseRuntimeUnavailable, reason: "lsp_runtime_capability_violation",
		},
		{
			name: "malformed frame", gatewayErr: errors.Join(errLSPGatewayFrame, errors.New("sensitive frame")),
			closeCode: lspCloseMalformed, reason: "lsp_message_malformed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			grant := lspWebSocketGrant(t)
			tickets := &lspWebSocketTicketsFake{grant: grant}
			gateway := &lspBindingGatewayFake{
				calls: make(chan lspGatewayCall, 1), err: test.gatewayErr,
			}
			server := newLSPWebSocketTestServer(t, tickets, gateway, LSPWebSocketConfig{
				AllowedOrigins: []string{"https://builder.example"}, BindTimeout: time.Second,
			})
			defer server.Close()
			connection, _, err := dialLSPWebSocket(
				server, "https://builder.example", []string{lsp.TicketSubprotocol},
				"?ticket="+lspWebSocketSecret, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			var hello lsp.ConnectionHello
			if err := connection.ReadJSON(&hello); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(validLSPClientBind(t, grant, hello.ConnectionID))
			if err != nil {
				t.Fatal(err)
			}
			if err := connection.WriteMessage(websocket.TextMessage, encoded); err != nil {
				t.Fatal(err)
			}
			select {
			case <-gateway.calls:
			case <-time.After(time.Second):
				t.Fatal("valid bind did not reach failing gateway")
			}
			_, _, readErr := connection.ReadMessage()
			var closeErr *websocket.CloseError
			if !errors.As(readErr, &closeErr) || closeErr.Code != test.closeCode ||
				closeErr.Text != test.reason || strings.Contains(closeErr.Text, "sensitive") {
				t.Fatalf("typed close=%v", readErr)
			}
		})
	}
}

func TestLSPWebSocketRejectsForgedForwardedProtoFromDirectPeer(t *testing.T) {
	tickets := &lspWebSocketTicketsFake{grant: lspWebSocketGrant(t)}
	gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
	server := newLSPWebSocketTestServer(t, tickets, gateway, LSPWebSocketConfig{
		AllowedOrigins: []string{"https://builder.example"}, RequireTLS: true,
	})
	defer server.Close()
	headers := make(http.Header)
	headers.Set("X-Forwarded-Proto", "https")
	connection, response, err := dialLSPWebSocket(
		server, "https://builder.example", []string{lsp.TicketSubprotocol},
		"?ticket="+lspWebSocketSecret, headers,
	)
	if connection != nil {
		connection.Close()
		t.Fatal("direct peer forged TLS with X-Forwarded-Proto")
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("forged XFP response=%v err=%v", response, err)
	}
	response.Body.Close()
	if tickets.callCount() != 0 {
		t.Fatalf("forged XFP consumed %d tickets", tickets.callCount())
	}
}

func TestLSPWebSocketUsesTrustedProxySchemeAndSameOrigin(t *testing.T) {
	tickets := &lspWebSocketTicketsFake{grant: lspWebSocketGrant(t)}
	gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
	handler, err := NewLSPWebSocketHandler(LSPWebSocketDependencies{
		Tickets: tickets, Gateway: gateway, Config: LSPWebSocketConfig{
			RequireTLS: true, TrustedProxies: []string{"127.0.0.1/32"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://builder.example"+lsp.TicketWebSocketPath, nil)
	request.Host = "builder.example"
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("Origin", "https://builder.example")
	request.Header.Set("X-Forwarded-Proto", "https")
	if !handler.originAllowed(request) || handler.effectiveRequestScheme(request) != "https" {
		t.Fatal("effective HTTPS same-origin request was rejected")
	}
	request.RemoteAddr = "203.0.113.9:43210"
	if handler.effectiveRequestScheme(request) != "http" || handler.originAllowed(request) {
		t.Fatal("untrusted proxy address influenced scheme or same-origin authority")
	}
	request.Header.Add("X-Forwarded-Proto", "http")
	if handler.effectiveRequestScheme(request) != "" {
		t.Fatal("multi-valued X-Forwarded-Proto was accepted")
	}
}

func TestLSPWebSocketTrustedProxyCanCompleteUpgrade(t *testing.T) {
	tickets := &lspWebSocketTicketsFake{grant: lspWebSocketGrant(t)}
	gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
	server := newLSPWebSocketTestServer(t, tickets, gateway, LSPWebSocketConfig{
		AllowedOrigins: []string{"https://builder.example"}, RequireTLS: true,
		TrustedProxies: []string{"127.0.0.1"}, BindTimeout: 100 * time.Millisecond,
	})
	defer server.Close()
	headers := make(http.Header)
	headers.Set("X-Forwarded-Proto", "https")
	connection, _, err := dialLSPWebSocket(
		server, "https://builder.example", []string{lsp.TicketSubprotocol},
		"?ticket="+lspWebSocketSecret, headers,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var hello lsp.ConnectionHello
	if err := connection.ReadJSON(&hello); err != nil || hello.Kind != "server.hello" {
		t.Fatalf("trusted proxy hello=%#v err=%v", hello, err)
	}
	if tickets.callCount() != 1 {
		t.Fatalf("trusted proxy consume calls=%d", tickets.callCount())
	}
}

func TestNewLSPWebSocketHandlerRejectsUnsafeConfiguration(t *testing.T) {
	tickets := &lspWebSocketTicketsFake{}
	gateway := &lspBindingGatewayFake{calls: make(chan lspGatewayCall, 1)}
	for _, dependencies := range []LSPWebSocketDependencies{
		{},
		{Tickets: tickets},
		{Tickets: tickets, Gateway: gateway, Config: LSPWebSocketConfig{BindTimeout: 5*time.Second + time.Nanosecond}},
		{Tickets: tickets, Gateway: gateway, Config: LSPWebSocketConfig{AllowedOrigins: []string{"*"}}},
		{Tickets: tickets, Gateway: gateway, Config: LSPWebSocketConfig{TrustedProxies: []string{"proxy.internal"}}},
		{Tickets: tickets, Gateway: gateway, Config: LSPWebSocketConfig{
			AllowedOrigins: []string{"https://builder.example", "https://BUILDER.EXAMPLE/"},
		}},
	} {
		if _, err := NewLSPWebSocketHandler(dependencies); err == nil {
			t.Fatalf("unsafe dependencies were accepted: %#v", dependencies)
		}
	}
}
