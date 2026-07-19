package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	modelResponsesPath = "/internal/agent-model/v1/responses"
	healthReadyPath    = "/health/ready"
	readyDocument      = "worksflow-agent-model-relay/v1\n"
	defaultReadyFile   = "/tmp/worksflow-agent-model-relay-ready"
	maximumHeaderBytes = 1 << 20
)

type relayConfig struct {
	ListenAddress   string
	TargetOrigin    *url.URL
	ReadyFile       string
	ShutdownTimeout time.Duration
	RequestTimeout  time.Duration
}

func main() {
	config, err := loadRelayConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := checkRelayHealth(config); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: worksflow-agent-model-relay [healthcheck]")
		os.Exit(64)
	}
	if err := serveRelay(config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadRelayConfig(getenv func(string) string) (relayConfig, error) {
	listenAddress := strings.TrimSpace(getenv("WORKSFLOW_MODEL_RELAY_LISTEN_ADDRESS"))
	targetValue := strings.TrimSpace(getenv("WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN"))
	readyFile := strings.TrimSpace(getenv("WORKSFLOW_MODEL_RELAY_READY_FILE"))
	if readyFile == "" {
		readyFile = defaultReadyFile
	}
	shutdownTimeout, err := parseRelayDuration(getenv("WORKSFLOW_MODEL_RELAY_SHUTDOWN_TIMEOUT"), 15*time.Second)
	if err != nil {
		return relayConfig{}, fmt.Errorf("invalid relay shutdown timeout: %w", err)
	}
	requestTimeout, err := parseRelayDuration(getenv("WORKSFLOW_MODEL_RELAY_REQUEST_TIMEOUT"), 8*time.Hour+time.Minute)
	if err != nil {
		return relayConfig{}, fmt.Errorf("invalid relay request timeout: %w", err)
	}
	if err := validateListenAddress(listenAddress); err != nil {
		return relayConfig{}, err
	}
	target, err := url.Parse(targetValue)
	if err != nil || targetValue != strings.TrimRight(targetValue, "/") || target.User != nil ||
		(target.Scheme != "http" && target.Scheme != "https") || target.Host == "" ||
		target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return relayConfig{}, errors.New("WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN must be one credential-free HTTP(S) origin")
	}
	if target.Hostname() == "" || strings.ContainsAny(target.Host, "\r\n\x00") {
		return relayConfig{}, errors.New("WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN has an invalid host")
	}
	if !filepath.IsAbs(readyFile) || filepath.Clean(readyFile) != readyFile || readyFile == "/" ||
		strings.ContainsAny(readyFile, "\r\n\x00") {
		return relayConfig{}, errors.New("WORKSFLOW_MODEL_RELAY_READY_FILE must be a clean absolute file path")
	}
	if shutdownTimeout < time.Second || shutdownTimeout > time.Minute ||
		requestTimeout < time.Second || requestTimeout > 8*time.Hour+time.Minute {
		return relayConfig{}, errors.New("relay timeout is outside its qualified bound")
	}
	return relayConfig{
		ListenAddress: listenAddress, TargetOrigin: target, ReadyFile: readyFile,
		ShutdownTimeout: shutdownTimeout, RequestTimeout: requestTimeout,
	}, nil
}

func parseRelayDuration(value string, fallback time.Duration) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func validateListenAddress(value string) error {
	host, portValue, err := net.SplitHostPort(value)
	port, portErr := strconv.Atoi(portValue)
	if err != nil || portErr != nil || net.ParseIP(host) == nil || port < 1024 || port > 65535 ||
		value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("WORKSFLOW_MODEL_RELAY_LISTEN_ADDRESS must be an explicit IP and unprivileged port")
	}
	return nil
}

func serveRelay(config relayConfig) error {
	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for Agent Model Relay: %w", err)
	}
	defer listener.Close()

	proxy := newModelReverseProxy(config)
	server := &http.Server{
		Handler:           relayHandler{proxy: proxy},
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    maximumHeaderBytes,
	}
	if err := writeReadyFile(config.ReadyFile); err != nil {
		return err
	}
	defer os.Remove(config.ReadyFile)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Agent Model Relay: %w", err)
	case <-ctx.Done():
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("shut down Agent Model Relay: %w", err)
	}
	if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve Agent Model Relay: %w", err)
	}
	return nil
}

func writeReadyFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create Agent Model Relay readiness file: %w", err)
	}
	if _, err := io.WriteString(file, readyDocument); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write Agent Model Relay readiness file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close Agent Model Relay readiness file: %w", err)
	}
	return nil
}

type relayHandler struct {
	proxy *httputil.ReverseProxy
}

func (handler relayHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method == http.MethodGet && request.URL.Path == healthReadyPath &&
		request.URL.RawQuery == "" && request.URL.Fragment == "" {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, readyDocument)
		return
	}
	if request.URL.Path != modelResponsesPath || request.URL.RawQuery != "" || request.URL.Fragment != "" {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if handler.proxy == nil {
		http.Error(writer, "relay unavailable", http.StatusServiceUnavailable)
		return
	}
	handler.proxy.ServeHTTP(writer, request)
}

func newModelReverseProxy(config relayConfig) *httputil.ReverseProxy {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: config.RequestTimeout,
		ExpectContinueTimeout: time.Second,
	}
	proxy := httputil.NewSingleHostReverseProxy(config.TargetOrigin)
	proxy.Transport = transport
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, err error) {
		slog.Error("Agent Model Relay upstream request failed", "error", err)
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		http.Error(writer, "model relay unavailable", http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		filterResponseHeaders(response.Header)
		return nil
	}
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalAuthorization := request.Header.Get("Authorization")
		originalContentType := request.Header.Get("Content-Type")
		originalAccept := request.Header.Get("Accept")
		originalBeta := request.Header.Get("OpenAI-Beta")
		originalDirector(request)
		request.URL.Path = modelResponsesPath
		request.URL.RawPath = ""
		request.URL.RawQuery = ""
		request.Host = config.TargetOrigin.Host
		request.Header = make(http.Header)
		// A nil slice is the ReverseProxy sentinel that suppresses automatic
		// X-Forwarded-For injection. Neither relay hop needs or trusts caller IPs.
		request.Header["X-Forwarded-For"] = nil
		copyRequestHeader(request.Header, "Authorization", originalAuthorization)
		copyRequestHeader(request.Header, "Content-Type", originalContentType)
		copyRequestHeader(request.Header, "Accept", originalAccept)
		copyRequestHeader(request.Header, "OpenAI-Beta", originalBeta)
	}
	return proxy
}

func copyRequestHeader(headers http.Header, name, value string) {
	value = strings.TrimSpace(value)
	if value != "" && !strings.ContainsAny(value, "\r\n\x00") {
		headers.Set(name, value)
	}
}

func filterResponseHeaders(headers http.Header) {
	allowed := map[string]bool{
		"Cache-Control": true, "Content-Type": true, "Openai-Request-Id": true,
		"Retry-After": true, "X-Content-Type-Options": true, "X-Request-Id": true,
	}
	for name := range headers {
		if !allowed[http.CanonicalHeaderKey(name)] {
			headers.Del(name)
		}
	}
}

func checkRelayHealth(config relayConfig) error {
	_, port, err := net.SplitHostPort(config.ListenAddress)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+healthReadyPath, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Agent Model Relay health request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(len(readyDocument)+1)))
	if err != nil || response.StatusCode != http.StatusOK || string(body) != readyDocument {
		return errors.New("Agent Model Relay is not ready")
	}
	return nil
}
