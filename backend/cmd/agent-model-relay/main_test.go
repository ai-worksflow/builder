package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadRelayConfigFailsClosed(t *testing.T) {
	valid := map[string]string{
		"WORKSFLOW_MODEL_RELAY_LISTEN_ADDRESS":   "0.0.0.0:18080",
		"WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN":    "http://api:8080",
		"WORKSFLOW_MODEL_RELAY_READY_FILE":       "/tmp/relay-ready",
		"WORKSFLOW_MODEL_RELAY_SHUTDOWN_TIMEOUT": "10s",
		"WORKSFLOW_MODEL_RELAY_REQUEST_TIMEOUT":  "30m",
	}
	getenv := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	config, err := loadRelayConfig(getenv(valid))
	if err != nil {
		t.Fatalf("valid relay config rejected: %v", err)
	}
	if config.ListenAddress != "0.0.0.0:18080" || config.TargetOrigin.String() != "http://api:8080" ||
		config.ReadyFile != "/tmp/relay-ready" || config.RequestTimeout != 30*time.Minute {
		t.Fatalf("relay config = %#v", config)
	}

	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing listen address", key: "WORKSFLOW_MODEL_RELAY_LISTEN_ADDRESS", value: ""},
		{name: "hostname listen address", key: "WORKSFLOW_MODEL_RELAY_LISTEN_ADDRESS", value: "localhost:18080"},
		{name: "privileged listen port", key: "WORKSFLOW_MODEL_RELAY_LISTEN_ADDRESS", value: "0.0.0.0:80"},
		{name: "target path", key: "WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN", value: "http://api:8080/internal"},
		{name: "target credentials", key: "WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN", value: "http://user:secret@api:8080"},
		{name: "target query", key: "WORKSFLOW_MODEL_RELAY_TARGET_ORIGIN", value: "http://api:8080?tenant=a"},
		{name: "relative ready file", key: "WORKSFLOW_MODEL_RELAY_READY_FILE", value: "relay-ready"},
		{name: "unbounded shutdown", key: "WORKSFLOW_MODEL_RELAY_SHUTDOWN_TIMEOUT", value: "2m"},
		{name: "unbounded request", key: "WORKSFLOW_MODEL_RELAY_REQUEST_TIMEOUT", value: "9h"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]string, len(valid))
			for key, value := range valid {
				values[key] = value
			}
			values[test.key] = test.value
			if _, err := loadRelayConfig(getenv(values)); err == nil {
				t.Fatalf("invalid relay config accepted: %s=%q", test.key, test.value)
			}
		})
	}
}

func TestRelayOnlyForwardsExactModelEndpoint(t *testing.T) {
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != modelResponsesPath || request.URL.RawQuery != "" {
			t.Errorf("upstream request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer attempt-capability" ||
			request.Header.Get("Content-Type") != "application/json" ||
			request.Header.Get("Accept") != "text/event-stream" ||
			request.Header.Get("OpenAI-Beta") != "responses=v1" {
			t.Errorf("upstream headers = %#v", request.Header)
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("X-Untrusted") != "" ||
			request.Header.Get("X-Forwarded-For") != "" {
			t.Errorf("unapproved request headers crossed relay: %#v", request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"model":"qualified"}` {
			t.Errorf("upstream body = %q", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("X-Request-ID", "provider-request")
		writer.Header().Set("Set-Cookie", "secret=must-not-cross")
		writer.Header().Set("X-Untrusted", "must-not-cross")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(writer, "data: accepted\n\n")
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(relayHandler{proxy: newModelReverseProxy(relayConfig{
		TargetOrigin: target, RequestTimeout: time.Minute,
	})})
	defer relay.Close()

	request, err := http.NewRequest(http.MethodPost, relay.URL+modelResponsesPath, strings.NewReader(`{"model":"qualified"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer attempt-capability")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("OpenAI-Beta", "responses=v1")
	request.Header.Set("Cookie", "browser=session")
	request.Header.Set("X-Untrusted", "must-not-cross")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted || string(body) != "data: accepted\n\n" {
		t.Fatalf("relay response = %d %q", response.StatusCode, body)
	}
	if response.Header.Get("X-Request-ID") != "provider-request" || response.Header.Get("Set-Cookie") != "" ||
		response.Header.Get("X-Untrusted") != "" {
		t.Fatalf("relay response headers = %#v", response.Header)
	}

	for _, path := range []string{"/api/platform/v1/projects", modelResponsesPath + "?debug=true"} {
		response, err := http.Post(relay.URL+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("POST %s status = %d", path, response.StatusCode)
		}
	}
	response, err = http.Get(relay.URL + modelResponsesPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("GET model endpoint status/allow = %d %q", response.StatusCode, response.Header.Get("Allow"))
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream request count = %d", requests.Load())
	}
}

func TestRelayHealthAndExclusiveReadyFile(t *testing.T) {
	recorder := httptest.NewRecorder()
	relayHandler{}.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, healthReadyPath, nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != readyDocument {
		t.Fatalf("health response = %d %q", recorder.Code, recorder.Body.String())
	}

	path := filepath.Join(t.TempDir(), "ready")
	if err := writeReadyFile(path); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(path)
	if err != nil || string(value) != readyDocument {
		t.Fatalf("ready file = %q, %v", value, err)
	}
	if err := writeReadyFile(path); err == nil {
		t.Fatal("stale readiness file was overwritten")
	}
}
