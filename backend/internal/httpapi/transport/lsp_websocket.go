package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/lsp"
)

const (
	defaultLSPBindTimeout = 5 * time.Second
	maximumLSPBindTimeout = 5 * time.Second
	defaultLSPWriteWait   = 2 * time.Second
	maximumLSPBindBytes   = int64(512 << 10)

	lspCloseMalformed          = 4400
	lspCloseDocumentStale      = 4409
	lspCloseRuntimeUnavailable = 4500
)

// LSPConnectionTicketConsumer atomically burns a one-time bearer before it
// rechecks the authority captured by the ticket.
type LSPConnectionTicketConsumer interface {
	Consume(context.Context, string, string) (lsp.TicketGrant, error)
}

// LSPBindingGateway is deliberately entered only after the transport has
// consumed a ticket and decoded one exact client.bind. Runtime and document
// access belong behind this boundary.
type LSPBindingGateway interface {
	ServeBoundLSP(context.Context, *websocket.Conn, lsp.TicketGrant, lsp.ClientBind) error
}

type LSPWebSocketConfig struct {
	AllowedOrigins []string
	TrustedProxies []string
	RequireTLS     bool
	BindTimeout    time.Duration
	WriteWait      time.Duration
}

type LSPWebSocketDependencies struct {
	Tickets LSPConnectionTicketConsumer
	Gateway LSPBindingGateway
	Config  LSPWebSocketConfig
}

type LSPWebSocketHandler struct {
	tickets        LSPConnectionTicketConsumer
	gateway        LSPBindingGateway
	allowedOrigins map[string]struct{}
	trustedProxies []*net.IPNet
	requireTLS     bool
	bindTimeout    time.Duration
	writeWait      time.Duration
	upgrader       websocket.Upgrader
}

func NewLSPWebSocketHandler(dependencies LSPWebSocketDependencies) (*LSPWebSocketHandler, error) {
	if dependencies.Tickets == nil || dependencies.Gateway == nil {
		return nil, errors.New("LSP connection tickets and bound gateway are required")
	}
	bindTimeout := dependencies.Config.BindTimeout
	if bindTimeout == 0 {
		bindTimeout = defaultLSPBindTimeout
	}
	if bindTimeout < time.Millisecond || bindTimeout > maximumLSPBindTimeout {
		return nil, errors.New("LSP bind timeout must be between one millisecond and five seconds")
	}
	writeWait := dependencies.Config.WriteWait
	if writeWait == 0 {
		writeWait = defaultLSPWriteWait
	}
	if writeWait < time.Millisecond || writeWait > maximumLSPBindTimeout {
		return nil, errors.New("LSP WebSocket write wait must be between one millisecond and five seconds")
	}
	allowedOrigins := make(map[string]struct{}, len(dependencies.Config.AllowedOrigins))
	for _, configured := range dependencies.Config.AllowedOrigins {
		origin, ok := canonicalLSPOrigin(configured)
		if !ok {
			return nil, errors.New("LSP allowed Origin is invalid")
		}
		if _, duplicate := allowedOrigins[origin]; duplicate {
			return nil, errors.New("LSP allowed Origin is duplicated")
		}
		allowedOrigins[origin] = struct{}{}
	}
	trustedProxies := make([]*net.IPNet, 0, len(dependencies.Config.TrustedProxies))
	trustedProxyKeys := make(map[string]struct{}, len(dependencies.Config.TrustedProxies))
	for _, configured := range dependencies.Config.TrustedProxies {
		network, key, ok := parseLSPTrustedProxy(configured)
		if !ok {
			return nil, errors.New("LSP trusted proxy CIDR or IP is invalid")
		}
		if _, duplicate := trustedProxyKeys[key]; duplicate {
			return nil, errors.New("LSP trusted proxy CIDR or IP is duplicated")
		}
		trustedProxyKeys[key] = struct{}{}
		trustedProxies = append(trustedProxies, network)
	}

	handler := &LSPWebSocketHandler{
		tickets: dependencies.Tickets, gateway: dependencies.Gateway,
		allowedOrigins: allowedOrigins, trustedProxies: trustedProxies,
		requireTLS:  dependencies.Config.RequireTLS,
		bindTimeout: bindTimeout, writeWait: writeWait,
	}
	handler.upgrader = websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096,
		Subprotocols: []string{lsp.TicketSubprotocol}, CheckOrigin: handler.originAllowed,
	}
	return handler, nil
}

func (handler *LSPWebSocketHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	ticket, ticketOK := exactLSPTicketQuery(request)
	redactLSPWebSocketQuery(request)
	if !validLSPUpgradeRequest(request) {
		writeLSPWebSocketProblem(writer, request, http.StatusBadRequest, "lsp_message_malformed",
			"A canonical WebSocket upgrade request is required.")
		return
	}
	effectiveScheme := handler.effectiveRequestScheme(request)
	if effectiveScheme == "" {
		writeLSPWebSocketProblem(writer, request, http.StatusBadRequest, "lsp_message_malformed",
			"The effective WebSocket request scheme is ambiguous or invalid.")
		return
	}
	if handler.requireTLS && effectiveScheme != "https" {
		writeLSPWebSocketProblem(writer, request, http.StatusForbidden, "lsp_forbidden",
			"TLS is required for the LSP WebSocket endpoint.")
		return
	}
	if !handler.originAllowed(request) {
		writeLSPWebSocketProblem(writer, request, http.StatusForbidden, "lsp_origin_forbidden",
			"The browser Origin is not authorized for this endpoint.")
		return
	}
	if !exactLSPSubprotocol(request) {
		writeLSPWebSocketProblem(writer, request, http.StatusBadRequest, "lsp_subprotocol_required",
			"Exactly one worksflow.sandbox-lsp.v1 subprotocol token is required.")
		return
	}
	if !ticketOK {
		writeLSPWebSocketProblem(writer, request, http.StatusUnauthorized, "lsp_ticket_required",
			"Exactly one single-use LSP connection ticket is required.")
		return
	}

	// Consume is intentionally the last operation before Upgrade. It atomically
	// burns the secret even when a subsequent authority check or Upgrade fails.
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	grant, err := handler.tickets.Consume(request.Context(), ticket, origin)
	if err != nil {
		status, code, detail := lspConsumeProblem(err)
		writeLSPWebSocketProblem(writer, request, status, code, detail)
		return
	}
	connection, err := handler.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	handler.serveConnection(request.Context(), connection, grant)
}

func redactLSPWebSocketQuery(request *http.Request) {
	if request == nil || request.URL == nil {
		return
	}
	request.URL.RawQuery = ""
	request.URL.ForceQuery = false
	request.RequestURI = request.URL.RequestURI()
}

func (handler *LSPWebSocketHandler) serveConnection(
	ctx context.Context,
	connection *websocket.Conn,
	grant lsp.TicketGrant,
) {
	connectionID := uuid.NewString()
	bindDeadline := time.Now().UTC().Add(handler.bindTimeout)
	hello, err := lsp.NewConnectionHello(grant, connectionID, bindDeadline)
	if err != nil {
		handler.close(connection, lspCloseRuntimeUnavailable, "lsp_runtime_unavailable")
		return
	}
	_ = connection.SetWriteDeadline(time.Now().Add(handler.writeWait))
	if err := connection.WriteJSON(hello); err != nil {
		return
	}
	connection.SetReadLimit(min(hello.Limits.MaxFrameBytes, maximumLSPBindBytes))
	_ = connection.SetReadDeadline(bindDeadline)
	messageType, value, err := connection.ReadMessage()
	if err != nil || messageType != websocket.TextMessage {
		handler.close(connection, lspCloseMalformed, "lsp_message_malformed")
		return
	}
	bind, err := lsp.DecodeClientBind(value, grant, connectionID)
	if err != nil {
		if errors.Is(err, lsp.ErrBindingStale) {
			handler.close(connection, lspCloseDocumentStale, "lsp_document_stale")
			return
		}
		handler.close(connection, lspCloseMalformed, bindProblemCode(err))
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	_ = connection.SetWriteDeadline(time.Time{})
	if err := handler.gateway.ServeBoundLSP(ctx, connection, grant, bind); err != nil {
		code, reason := lspGatewayClose(err)
		handler.close(connection, code, reason)
		return
	}
	handler.close(connection, websocket.CloseNormalClosure, "connection closed")
}

func lspGatewayClose(err error) (int, string) {
	switch {
	case errors.Is(err, lsp.ErrGatewaySecurityUnavailable):
		return lspCloseRuntimeUnavailable, "lsp_security_unavailable"
	case errors.Is(err, lsp.ErrGatewayEditorLeaseConflict):
		return lspCloseDocumentStale, "lsp_editor_conflict"
	case errors.Is(err, lsp.ErrGatewayEditorLeaseLost):
		return lspCloseDocumentStale, "lsp_editor_lease_lost"
	case errors.Is(err, lsp.ErrGatewayStale):
		return lspCloseDocumentStale, "lsp_head_stale"
	case errors.Is(err, lsp.ErrGatewayServerViolation):
		return lspCloseRuntimeUnavailable, "lsp_runtime_capability_violation"
	case errors.Is(err, errLSPGatewayFrame):
		return lspCloseMalformed, "lsp_message_malformed"
	case errors.Is(err, lsp.ErrGatewayClosed), errors.Is(err, context.Canceled):
		return websocket.CloseNormalClosure, "connection closed"
	default:
		return lspCloseRuntimeUnavailable, "lsp_runtime_unavailable"
	}
}

func (handler *LSPWebSocketHandler) close(connection *websocket.Conn, code int, reason string) {
	deadline := time.Now().Add(handler.writeWait)
	_ = connection.SetWriteDeadline(deadline)
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), deadline)
}

func (handler *LSPWebSocketHandler) originAllowed(request *http.Request) bool {
	if request == nil {
		return false
	}
	values := request.Header.Values("Origin")
	if len(values) != 1 {
		return false
	}
	raw := strings.TrimSpace(values[0])
	origin, ok := canonicalLSPOrigin(raw)
	if !ok {
		return false
	}
	if handler.sameOrigin(request, origin) {
		return true
	}
	_, allowed := handler.allowedOrigins[origin]
	return allowed
}

func validLSPUpgradeRequest(request *http.Request) bool {
	if request == nil || request.Method != http.MethodGet || !request.ProtoAtLeast(1, 1) ||
		!websocket.IsWebSocketUpgrade(request) {
		return false
	}
	versions := request.Header.Values("Sec-WebSocket-Version")
	keys := request.Header.Values("Sec-WebSocket-Key")
	if len(versions) != 1 || strings.TrimSpace(versions[0]) != "13" || len(keys) != 1 {
		return false
	}
	key := strings.TrimSpace(keys[0])
	decoded, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(decoded) == 16 && base64.StdEncoding.EncodeToString(decoded) == key
}

func exactLSPSubprotocol(request *http.Request) bool {
	if request == nil {
		return false
	}
	var protocols []string
	for _, header := range request.Header.Values("Sec-WebSocket-Protocol") {
		for _, value := range strings.Split(header, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				return false
			}
			protocols = append(protocols, value)
		}
	}
	return len(protocols) == 1 && protocols[0] == lsp.TicketSubprotocol
}

func exactLSPTicketQuery(request *http.Request) (string, bool) {
	if request == nil || request.URL == nil {
		return "", false
	}
	const prefix = "ticket="
	raw := request.URL.RawQuery
	if request.URL.ForceQuery || request.URL.Fragment != "" || !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	value := strings.TrimPrefix(raw, prefix)
	if len(value) != 43 || strings.ContainsAny(value, "&;%+ ") {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", false
	}
	return value, true
}

func canonicalLSPOrigin(value string) (string, bool) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Opaque != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" || strings.ContainsAny(parsed.Host, "\r\n\x00") {
		return "", false
	}
	if parsed.Scheme == "http" && !localLSPHost(hostname) {
		return "", false
	}
	host := hostname
	if parsed.Port() != "" {
		host = net.JoinHostPort(hostname, parsed.Port())
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return parsed.Scheme + "://" + host, true
}

func localLSPHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func parseLSPTrustedProxy(value string) (*net.IPNet, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, "", false
	}
	if address := net.ParseIP(value); address != nil {
		bits := 128
		if ipv4 := address.To4(); ipv4 != nil {
			address, bits = ipv4, 32
		}
		network := &net.IPNet{IP: address, Mask: net.CIDRMask(bits, bits)}
		return network, network.String(), true
	}
	address, network, err := net.ParseCIDR(value)
	if err != nil || address == nil || network == nil {
		return nil, "", false
	}
	if ipv4 := address.To4(); ipv4 != nil {
		ones, bits := network.Mask.Size()
		if bits != 32 {
			return nil, "", false
		}
		network = &net.IPNet{IP: ipv4.Mask(net.CIDRMask(ones, bits)), Mask: net.CIDRMask(ones, bits)}
	} else {
		ones, bits := network.Mask.Size()
		if bits != 128 {
			return nil, "", false
		}
		network = &net.IPNet{IP: address.Mask(net.CIDRMask(ones, bits)), Mask: net.CIDRMask(ones, bits)}
	}
	return network, network.String(), true
}

func (handler *LSPWebSocketHandler) effectiveRequestScheme(request *http.Request) string {
	if request == nil {
		return ""
	}
	forwardedValues := request.Header.Values("X-Forwarded-Proto")
	forwarded := ""
	if len(forwardedValues) != 0 {
		if len(forwardedValues) != 1 || strings.Contains(forwardedValues[0], ",") {
			return ""
		}
		forwarded = strings.ToLower(strings.TrimSpace(forwardedValues[0]))
		if forwarded != "http" && forwarded != "https" {
			return ""
		}
		if handler.remoteAddressTrusted(request.RemoteAddr) {
			return forwarded
		}
	}
	if request.TLS != nil {
		return "https"
	}
	return "http"
}

func (handler *LSPWebSocketHandler) remoteAddressTrusted(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(remoteAddress), "[]")
	}
	address := net.ParseIP(host)
	if address == nil {
		return false
	}
	for _, network := range handler.trustedProxies {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

func (handler *LSPWebSocketHandler) sameOrigin(request *http.Request, origin string) bool {
	if request == nil || strings.TrimSpace(request.Host) == "" || strings.ContainsAny(request.Host, "/\\@") {
		return false
	}
	scheme := handler.effectiveRequestScheme(request)
	if scheme == "" {
		return false
	}
	effectiveOrigin, ok := canonicalLSPOrigin(scheme + "://" + strings.TrimSpace(request.Host))
	return ok && effectiveOrigin == origin
}

func lspConsumeProblem(err error) (int, string, string) {
	switch {
	case errors.Is(err, lsp.ErrOriginForbidden):
		return http.StatusForbidden, "lsp_origin_forbidden", "The consumed ticket does not authorize this browser Origin."
	case errors.Is(err, lsp.ErrForbidden):
		return http.StatusForbidden, "lsp_forbidden", "The consumed ticket no longer has the required project or mode authority."
	case errors.Is(err, lsp.ErrSessionNotReady):
		return http.StatusConflict, "lsp_session_not_ready", "The consumed ticket no longer names a ready SandboxSession."
	case errors.Is(err, lsp.ErrHeadStale):
		return http.StatusConflict, "lsp_head_stale", "The consumed ticket no longer matches the authoritative Candidate head."
	case errors.Is(err, lsp.ErrProfileNotDeclared):
		return http.StatusUnprocessableEntity, "lsp_profile_not_declared", "The consumed ticket no longer names an admitted language-server profile."
	case errors.Is(err, lsp.ErrRateLimited):
		return http.StatusTooManyRequests, "lsp_rate_limited", "The bounded LSP admission rate has been exceeded."
	case errors.Is(err, lsp.ErrTicketUnavailable), errors.Is(err, lsp.ErrAuthorityUnavailable):
		return http.StatusServiceUnavailable, "lsp_ticket_store_unavailable", "The LSP ticket authority is temporarily unavailable."
	case errors.Is(err, lsp.ErrAuditUnavailable):
		return http.StatusServiceUnavailable, "lsp_ticket_store_unavailable", "The mandatory LSP security audit is temporarily unavailable."
	default:
		return http.StatusUnauthorized, "lsp_ticket_rejected", "The LSP connection ticket is invalid, expired, consumed, or unknown."
	}
}

func bindProblemCode(err error) string {
	if errors.Is(err, lsp.ErrProfileNotDeclared) {
		return "lsp_profile_not_declared"
	}
	return "lsp_message_malformed"
}

func writeLSPWebSocketProblem(
	writer http.ResponseWriter,
	request *http.Request,
	status int,
	code, detail string,
) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	instance := ""
	if request != nil && request.URL != nil {
		// The query may contain a bearer secret and must never enter the response.
		instance = request.URL.Path
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"type": "urn:worksflow:problem:" + code, "title": http.StatusText(status),
		"status": status, "detail": detail, "instance": instance, "code": code,
		"requestId": writer.Header().Get("X-Request-ID"),
	})
}

var _ http.Handler = (*LSPWebSocketHandler)(nil)
