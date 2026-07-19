package transport

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

const previewTestSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type previewResolverFake struct {
	target sandbox.PreviewTarget
	err    error
	secret string
}

func (resolver *previewResolverFake) Resolve(_ context.Context, secret string) (sandbox.PreviewTarget, error) {
	resolver.secret = secret
	return resolver.target, resolver.err
}

func TestSandboxPreviewHandlerIsolatesHeadersCookiesAndRedirects(t *testing.T) {
	seen := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers := request.Header.Clone()
		headers.Set("Observed-Host", request.Host)
		seen <- headers
		writer.Header().Add("Set-Cookie", "app=value; Path=/; HttpOnly")
		writer.Header().Add("Set-Cookie", "poison=value; Domain=preview.localhost; Path=/")
		writer.Header().Set("Location", "http://localhost:3000/next")
		writer.Header().Set("X-Sandbox-Session-Epoch", "leak")
		writer.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	address := strings.TrimPrefix(upstream.URL, "http://")
	resolver := &previewResolverFake{target: previewTransportTarget(address)}
	nextCalled := false
	handler := previewTransportHandler(t, resolver, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}), "http://preview.localhost:8080")

	request := httptest.NewRequest(http.MethodGet, "http://placeholder/path?q=1", nil)
	request.Host = previewTestSecret + ".preview.localhost:8080"
	request.Header.Set("Origin", "http://"+request.Host)
	request.Header.Set("Cookie", "worksflow_session=platform-secret; app_session=allowed")
	request.Header.Set("X-Sandbox-Session-Epoch", "9")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if nextCalled || resolver.secret != previewTestSecret || response.Code != http.StatusFound ||
		response.Header().Get("Location") != "http://"+request.Host+"/next" {
		t.Fatalf("preview routing failed: next=%v secret=%q status=%d headers=%v body=%s", nextCalled, resolver.secret, response.Code, response.Header(), response.Body.String())
	}
	upstreamHeaders := <-seen
	if upstreamHeaders.Get("Observed-Host") != "localhost:3000" ||
		upstreamHeaders.Get("Origin") != "http://localhost:3000" ||
		upstreamHeaders.Get("X-Sandbox-Session-Epoch") != "" ||
		strings.Contains(upstreamHeaders.Get("Cookie"), "platform-secret") ||
		!strings.Contains(upstreamHeaders.Get("Cookie"), "app_session=allowed") {
		t.Fatalf("unsafe upstream headers = %#v", upstreamHeaders)
	}
	cookies := response.Header().Values("Set-Cookie")
	if len(cookies) != 1 || !strings.HasPrefix(cookies[0], "app=value") {
		t.Fatalf("parent-domain cookie was not removed: %#v", cookies)
	}
	if response.Header().Get("X-Sandbox-Session-Epoch") != "" ||
		!strings.Contains(response.Header().Get("Content-Security-Policy"), "frame-ancestors https://builder.example") ||
		response.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("preview security headers = %#v", response.Header())
	}
}

func TestSandboxPreviewHandlerPassesHMRWebSocketOnSameCapabilityHost(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(request *http.Request) bool {
		return request.Header.Get("Origin") == "http://localhost:3000"
	}}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		messageType, value, err := connection.ReadMessage()
		if err == nil {
			_ = connection.WriteMessage(messageType, append([]byte("hmr:"), value...))
		}
	}))
	defer upstream.Close()

	server := httptest.NewUnstartedServer(nil)
	serverPort := server.Listener.Addr().(*net.TCPAddr).Port
	publicOrigin := "http://preview.localhost:" + strconv.Itoa(serverPort)
	resolver := &previewResolverFake{target: previewTransportTarget(strings.TrimPrefix(upstream.URL, "http://"))}
	server.Config.Handler = previewTransportHandler(t, resolver, http.NotFoundHandler(), publicOrigin)
	server.Start()
	defer server.Close()

	previewHost := previewTestSecret + ".preview.localhost:" + strconv.Itoa(serverPort)
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		},
	}
	headers := http.Header{"Origin": []string{"http://" + previewHost}}
	connection, response, err := dialer.Dial("ws://"+previewHost+"/@vite/client", headers)
	if err != nil {
		body := ""
		if response != nil && response.Body != nil {
			value, _ := io.ReadAll(response.Body)
			body = string(value)
		}
		t.Fatalf("dial preview HMR: %v status=%v body=%s", err, response, body)
	}
	defer connection.Close()
	if err := connection.WriteMessage(websocket.TextMessage, []byte("update")); err != nil {
		t.Fatal(err)
	}
	_, value, err := connection.ReadMessage()
	if err != nil || string(value) != "hmr:update" || resolver.secret != previewTestSecret {
		t.Fatalf("HMR response=%q secret=%q err=%v", value, resolver.secret, err)
	}
}

func TestSandboxPreviewHandlerRejectsWrongHostAndExpiredCapability(t *testing.T) {
	resolver := &previewResolverFake{err: sandbox.ErrPreviewGrantExpired}
	nextCalls := 0
	handler := previewTransportHandler(t, resolver, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		nextCalls++
		writer.WriteHeader(http.StatusNoContent)
	}), "https://preview.example:8443")

	platform := httptest.NewRequest(http.MethodGet, "https://platform.example/v1/session", nil)
	platform.Host = "platform.example"
	platformResponse := httptest.NewRecorder()
	handler.ServeHTTP(platformResponse, platform)
	if nextCalls != 1 || platformResponse.Code != http.StatusNoContent {
		t.Fatalf("platform request did not pass through: calls=%d status=%d", nextCalls, platformResponse.Code)
	}

	expired := httptest.NewRequest(http.MethodGet, "https://placeholder/", nil)
	expired.Host = previewTestSecret + ".preview.example:8443"
	expiredResponse := httptest.NewRecorder()
	handler.ServeHTTP(expiredResponse, expired)
	if expiredResponse.Code != http.StatusGone || expiredResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expired status=%d headers=%v body=%s", expiredResponse.Code, expiredResponse.Header(), expiredResponse.Body.String())
	}

	wrongPort := httptest.NewRequest(http.MethodGet, "https://placeholder/", nil)
	wrongPort.Host = previewTestSecret + ".preview.example:9443"
	wrongPortResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongPortResponse, wrongPort)
	if wrongPortResponse.Code != http.StatusMisdirectedRequest {
		t.Fatalf("wrong preview origin port status=%d body=%s", wrongPortResponse.Code, wrongPortResponse.Body.String())
	}
}

func previewTransportHandler(
	t *testing.T,
	resolver SandboxPreviewResolver,
	next http.Handler,
	publicOrigin string,
) *SandboxPreviewHandler {
	t.Helper()
	handler, err := NewSandboxPreviewHandler(
		next, resolver, publicOrigin, []string{"https://builder.example"},
		[]string{"worksflow_session", "worksflow_csrf"}, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func previewTransportTarget(address string) sandbox.PreviewTarget {
	return sandbox.PreviewTarget{
		Grant: sandbox.PreviewGrant{
			SchemaVersion: sandbox.PreviewGrantSchemaVersion,
			ID:            "10000000-0000-4000-8000-000000000001",
			ProjectID:     "10000000-0000-4000-8000-000000000002",
			SessionID:     "10000000-0000-4000-8000-000000000003",
			SessionEpoch:  1, ActorID: "10000000-0000-4000-8000-000000000004",
			PortName: "web-http", PortNumber: 3000, Protocol: "http",
			IssuedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Minute),
		},
		Port: sandbox.PortView{
			SchemaVersion: sandbox.PortSchemaVersion, Name: "web-http", ServiceID: "web",
			Number: 3000, Protocol: "http", State: sandbox.PortListening, Healthy: true, Previewable: true,
		},
		Address: address, ExpiresAt: time.Now().Add(time.Minute),
	}
}
