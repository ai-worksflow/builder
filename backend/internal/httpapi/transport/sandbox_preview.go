package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/sandbox"
)

type SandboxPreviewResolver interface {
	Resolve(context.Context, string) (sandbox.PreviewTarget, error)
}

type SandboxPreviewHandler struct {
	next            http.Handler
	resolver        SandboxPreviewResolver
	publicOrigin    *url.URL
	frameAncestors  string
	platformCookies map[string]bool
	logger          *slog.Logger
}

func NewSandboxPreviewHandler(
	next http.Handler,
	resolver SandboxPreviewResolver,
	publicOrigin string,
	platformOrigins []string,
	platformCookieNames []string,
	logger *slog.Logger,
) (*SandboxPreviewHandler, error) {
	parsed, err := url.Parse(strings.TrimSpace(publicOrigin))
	if next == nil || resolver == nil || logger == nil || err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("preview next handler, resolver, public origin, and logger are required")
	}
	cookies := make(map[string]bool, len(platformCookieNames))
	for _, name := range platformCookieNames {
		if name = strings.TrimSpace(name); name != "" {
			cookies[name] = true
		}
	}
	return &SandboxPreviewHandler{
		next: next, resolver: resolver, publicOrigin: parsed,
		frameAncestors: previewFrameAncestors(platformOrigins), platformCookies: cookies,
		logger: logger,
	}, nil
}

func (handler *SandboxPreviewHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	secret, previewHost, valid := handler.previewSecret(request.Host)
	if !previewHost {
		handler.next.ServeHTTP(writer, request)
		return
	}
	handler.writePreviewHeaders(writer.Header())
	if !valid {
		http.Error(writer, "preview capability host is invalid", http.StatusMisdirectedRequest)
		return
	}
	target, err := handler.resolver.Resolve(request.Context(), secret)
	if err != nil {
		handler.writeResolutionError(writer, request, err)
		return
	}
	handler.proxy(target, request.Host).ServeHTTP(writer, request)
}

func (handler *SandboxPreviewHandler) proxy(target sandbox.PreviewTarget, publicHost string) http.Handler {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connection, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", target.Address)
			if err != nil {
				return nil, err
			}
			if err := connection.SetDeadline(target.ExpiresAt); err != nil {
				_ = connection.Close()
				return nil, err
			}
			return connection, nil
		},
		TLSClientConfig:    &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, // #nosec G402 -- the dialer is pinned to an exact isolated sandbox port.
		ForceAttemptHTTP2:  false,
		MaxIdleConns:       16,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}
	upstreamHost := net.JoinHostPort("localhost", strconv.Itoa(target.Port.Number))
	publicOrigin := handler.publicOrigin.Scheme + "://" + publicHost
	proxy := &httputil.ReverseProxy{
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			proxyRequest.SetURL(&url.URL{Scheme: target.Port.Protocol, Host: "sandbox-upstream"})
			proxyRequest.Out.Host = upstreamHost
			stripPreviewControlHeaders(proxyRequest.Out.Header)
			stripNamedCookies(proxyRequest.Out.Header, handler.platformCookies)
			if origin := strings.TrimSuffix(proxyRequest.In.Header.Get("Origin"), "/"); origin == strings.TrimSuffix(publicOrigin, "/") {
				proxyRequest.Out.Header.Set("Origin", target.Port.Protocol+"://"+upstreamHost)
			}
		},
		Transport:     transport,
		FlushInterval: -1,
		ModifyResponse: func(response *http.Response) error {
			handler.sanitizeResponse(response, publicHost, target)
			return nil
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			handler.writePreviewHeaders(writer.Header())
			handler.logger.Warn("sandbox preview upstream failed",
				"session_id", target.Grant.SessionID, "port", target.Port.Name,
				"request_path", request.URL.EscapedPath(), "error", err)
			http.Error(writer, "sandbox preview upstream is unavailable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer transport.CloseIdleConnections()
		proxy.ServeHTTP(writer, request)
	})
}

func (handler *SandboxPreviewHandler) previewSecret(requestHost string) (string, bool, bool) {
	host, port, err := net.SplitHostPort(requestHost)
	if err != nil {
		if strings.Contains(requestHost, ":") {
			candidate := strings.ToLower(strings.TrimSpace(requestHost))
			return "", candidate == strings.ToLower(handler.publicOrigin.Host) || strings.Contains(candidate, "."+strings.ToLower(handler.publicOrigin.Hostname())), false
		}
		host = requestHost
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	baseHost := strings.ToLower(strings.TrimSuffix(handler.publicOrigin.Hostname(), "."))
	if host != baseHost && !strings.HasSuffix(host, "."+baseHost) {
		return "", false, false
	}
	expectedPort := handler.publicOrigin.Port()
	if expectedPort != port {
		if !(expectedPort == "" && port == "") {
			return "", true, false
		}
	}
	if host == baseHost {
		return "", true, false
	}
	secret := strings.TrimSuffix(host, "."+baseHost)
	if strings.Contains(secret, ".") || !validPreviewHostSecret(secret) {
		return "", true, false
	}
	return secret, true, true
}

func (handler *SandboxPreviewHandler) sanitizeResponse(
	response *http.Response,
	publicHost string,
	target sandbox.PreviewTarget,
) {
	stripPreviewControlHeaders(response.Header)
	response.Header.Del("X-Frame-Options")
	response.Header.Del("Content-Security-Policy")
	response.Header.Del("Content-Security-Policy-Report-Only")
	response.Header.Del("Server")
	response.Header.Del("X-Powered-By")
	sanitizePreviewCookies(response.Header)
	if location := response.Header.Get("Location"); location != "" {
		if rewritten, ok := rewritePreviewLocation(location, handler.publicOrigin.Scheme, publicHost, target.Port.Number); ok {
			response.Header.Set("Location", rewritten)
		}
	}
	handler.writePreviewHeaders(response.Header)
}

func (handler *SandboxPreviewHandler) writePreviewHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'self' data: blob:; script-src 'self' 'unsafe-inline' 'unsafe-eval' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self' ws: wss:; worker-src 'self' blob:; frame-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors "+handler.frameAncestors)
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=()")
	header.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
}

func (handler *SandboxPreviewHandler) writeResolutionError(writer http.ResponseWriter, request *http.Request, err error) {
	status := http.StatusBadGateway
	message := "sandbox preview is unavailable"
	switch {
	case errors.Is(err, sandbox.ErrPreviewGrantInvalid), errors.Is(err, sandbox.ErrPortNotFound):
		status, message = http.StatusNotFound, "sandbox preview was not found"
	case errors.Is(err, sandbox.ErrPreviewGrantExpired), errors.Is(err, sandbox.ErrEpochFenced):
		status, message = http.StatusGone, "sandbox preview capability has expired"
	case errors.Is(err, sandbox.ErrPortNotReady), errors.Is(err, sandbox.ErrRuntimeNotReady):
		status, message = http.StatusServiceUnavailable, "sandbox preview is not ready"
	case errors.Is(err, sandbox.ErrPreviewGrantUnavailable), errors.Is(err, sandbox.ErrRuntimeUnavailable):
		status, message = http.StatusServiceUnavailable, "sandbox preview service is unavailable"
	}
	handler.logger.Warn("sandbox preview resolution failed", "host", request.Host, "error", err)
	http.Error(writer, message, status)
}

func validPreviewHostSecret(value string) bool {
	if len(value) != 48 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func previewFrameAncestors(origins []string) string {
	values := make([]string, 0, len(origins))
	seen := map[string]bool{}
	for _, value := range origins {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.User != nil || parsed.Host == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		origin := parsed.Scheme + "://" + parsed.Host
		if !seen[origin] {
			seen[origin] = true
			values = append(values, origin)
		}
	}
	if len(values) == 0 {
		return "'none'"
	}
	return strings.Join(values, " ")
}

func stripPreviewControlHeaders(header http.Header) {
	for name := range header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-sandbox-") || strings.HasPrefix(lower, "x-candidate-") ||
			lower == "x-csrf-token" || lower == "x-request-id" || lower == "x-writer-lease-epoch" ||
			lower == "x-forwarded-host" || lower == "x-forwarded-proto" || lower == "x-forwarded-for" ||
			lower == "x-real-ip" || lower == "proxy-authorization" {
			header.Del(name)
		}
	}
}

func stripNamedCookies(header http.Header, denied map[string]bool) {
	if len(denied) == 0 {
		return
	}
	request := &http.Request{Header: header}
	kept := make([]string, 0)
	for _, cookie := range request.Cookies() {
		if !denied[cookie.Name] {
			kept = append(kept, cookie.String())
		}
	}
	header.Del("Cookie")
	if len(kept) > 0 {
		header.Set("Cookie", strings.Join(kept, "; "))
	}
}

func sanitizePreviewCookies(header http.Header) {
	values := header.Values("Set-Cookie")
	header.Del("Set-Cookie")
	for _, value := range values {
		unsafeDomain := false
		for _, part := range strings.Split(value, ";")[1:] {
			if strings.EqualFold(strings.TrimSpace(strings.SplitN(part, "=", 2)[0]), "Domain") {
				unsafeDomain = true
				break
			}
		}
		if !unsafeDomain {
			header.Add("Set-Cookie", value)
		}
	}
}

func rewritePreviewLocation(value, publicScheme, publicHost string, upstreamPort int) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() {
		return value, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "0.0.0.0" &&
		host != "sandbox-port" && host != "sandbox-upstream" {
		return value, false
	}
	if port := parsed.Port(); port != "" && port != strconv.Itoa(upstreamPort) {
		return value, false
	}
	parsed.Scheme, parsed.Host = publicScheme, publicHost
	return parsed.String(), true
}

var _ http.Handler = (*SandboxPreviewHandler)(nil)
var _ SandboxPreviewResolver = (*sandbox.PortService)(nil)
