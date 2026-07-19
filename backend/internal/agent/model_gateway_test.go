package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type modelGatewayCapabilitiesFake struct {
	record                ModelCapabilityRecord
	token                 string
	authenticated         int
	acquired              int
	released              int
	inputReserved         int64
	outputRequested       int64
	lease                 ModelBudgetLease
	acquireErr            error
	releaseUsage          int64
	releaseUsageAvailable bool
}

func (capabilities *modelGatewayCapabilitiesFake) Authenticate(
	_ context.Context,
	token string,
) (ModelCapabilityRecord, error) {
	if token != capabilities.token {
		return ModelCapabilityRecord{}, ErrModelCapability
	}
	capabilities.authenticated++
	return capabilities.record, nil
}

func (capabilities *modelGatewayCapabilitiesFake) Acquire(
	_ context.Context,
	_ ModelCapabilityRecord,
	_ time.Duration,
	inputTokenUpperBound, requestedOutputTokens int64,
) (ModelBudgetLease, error) {
	capabilities.acquired++
	capabilities.inputReserved = inputTokenUpperBound
	capabilities.outputRequested = requestedOutputTokens
	if capabilities.acquireErr != nil {
		return ModelBudgetLease{}, capabilities.acquireErr
	}
	lease := capabilities.lease
	if lease.ID == "" {
		lease = ModelBudgetLease{
			ID: uuid.NewString(), InputTokenUpperBound: inputTokenUpperBound,
			OutputTokenAllowance: requestedOutputTokens,
		}
	}
	return lease, nil
}

func (capabilities *modelGatewayCapabilitiesFake) Release(
	_ context.Context,
	_ ModelCapabilityRecord,
	_ ModelBudgetLease,
	observedOutputTokens int64,
	usageAvailable bool,
) error {
	capabilities.released++
	capabilities.releaseUsage = observedOutputTokens
	capabilities.releaseUsageAvailable = usageAvailable
	return nil
}

func TestModelGatewayReplacesCapabilityWithUpstreamSecretAndForcesEphemeralRequest(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		if request.Header.Get("Authorization") != "Bearer upstream-platform-secret" {
			t.Fatalf("upstream Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Cookie") != "" {
			t.Fatalf("browser/Runner Cookie reached upstream: %q", request.Header.Get("Cookie"))
		}
		var body map[string]any
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "qualified-model" || body["store"] != false ||
			body["max_output_tokens"].(json.Number).String() != "4096" {
			t.Fatalf("qualified upstream body = %#v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":123}}}\n\n")
	}))
	defer upstream.Close()
	capabilities := modelGatewayCapabilitiesFixture()
	gateway, err := NewModelGateway(ModelGatewayConfig{
		UpstreamResponsesURL: upstream.URL + "/v1/responses",
		UpstreamAPIKey:       "upstream-platform-secret",
		MaxInputBytes:        1 << 20,
		MaxOutputBytes:       1 << 20,
		RequestTimeout:       time.Minute,
	}, capabilities, upstream.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"http://gateway.internal/internal/agent-model/v1/responses",
		strings.NewReader(`{"model":"qualified-model","input":"implement"}`),
	)
	request.Header.Set("Authorization", "Bearer "+capabilities.token)
	request.Header.Set("Cookie", "worksflow_session=must-not-forward")
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "response.completed") || !response.Flushed {
		t.Fatalf("gateway response code=%d body=%s", response.Code, response.Body.String())
	}
	if upstreamCalls != 1 || capabilities.authenticated != 1 || capabilities.acquired != 1 || capabilities.released != 1 {
		t.Fatalf("gateway calls upstream=%d capabilities=%+v", upstreamCalls, capabilities)
	}
	if capabilities.inputReserved <= int64(len(`{"model":"qualified-model","input":"implement"}`)) ||
		capabilities.outputRequested != 4096 || capabilities.releaseUsage != 123 ||
		!capabilities.releaseUsageAvailable {
		t.Fatalf("gateway budget accounting=%+v", capabilities)
	}
}

func TestModelGatewayRejectsModelStateAndBudgetEscapesBeforeUpstream(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		upstreamCalls++
	}))
	defer upstream.Close()
	for _, body := range []string{
		`{"model":"another-model","input":"x"}`,
		`{"model":"qualified-model","store":true,"input":"x"}`,
		`{"model":"qualified-model","conversation":"persistent","input":"x"}`,
		`{"model":"qualified-model","max_output_tokens":4097,"input":"x"}`,
	} {
		capabilities := modelGatewayCapabilitiesFixture()
		gateway, err := NewModelGateway(ModelGatewayConfig{
			UpstreamResponsesURL: upstream.URL + "/v1/responses",
			UpstreamAPIKey:       "upstream-platform-secret",
			MaxInputBytes:        1 << 20,
			MaxOutputBytes:       1 << 20,
			RequestTimeout:       time.Minute,
		}, capabilities, upstream.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/internal/agent-model/v1/responses", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+capabilities.token)
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "model_request_policy_denied") {
			t.Fatalf("body=%s code=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("policy-denied requests reached upstream %d times", upstreamCalls)
	}
}

func TestModelGatewayClampsRequestToRemainingOutputBudget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["max_output_tokens"].(json.Number).String() != "321" {
			t.Fatalf("remaining output allowance was not enforced: %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"response-1","usage":{"output_tokens":17}}`)
	}))
	defer upstream.Close()
	capabilities := modelGatewayCapabilitiesFixture()
	capabilities.lease = ModelBudgetLease{
		ID: uuid.NewString(), InputTokenUpperBound: 5000, OutputTokenAllowance: 321,
	}
	gateway, err := NewModelGateway(ModelGatewayConfig{
		UpstreamResponsesURL: upstream.URL + "/v1/responses", UpstreamAPIKey: "secret",
		MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, RequestTimeout: time.Minute,
	}, capabilities, upstream.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-model/v1/responses", strings.NewReader(
		`{"model":"qualified-model","max_output_tokens":4096,"input":"x"}`,
	))
	request.Header.Set("Authorization", "Bearer "+capabilities.token)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK || capabilities.releaseUsage != 17 || !capabilities.releaseUsageAvailable {
		t.Fatalf("response=%d %s accounting=%+v", response.Code, response.Body.String(), capabilities)
	}
}

func TestModelGatewayRejectsConservativeCumulativeInputBudgetBeforeUpstream(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		upstreamCalls++
	}))
	defer upstream.Close()
	capabilities := modelGatewayCapabilitiesFixture()
	capabilities.acquireErr = ErrModelInputBudgetExceeded
	gateway, err := NewModelGateway(ModelGatewayConfig{
		UpstreamResponsesURL: upstream.URL + "/v1/responses", UpstreamAPIKey: "secret",
		MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, RequestTimeout: time.Minute,
	}, capabilities, upstream.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-model/v1/responses", strings.NewReader(
		`{"model":"qualified-model","input":"x"}`,
	))
	request.Header.Set("Authorization", "Bearer "+capabilities.token)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests ||
		!strings.Contains(response.Body.String(), "model_input_budget_exceeded") ||
		upstreamCalls != 0 || capabilities.released != 0 {
		t.Fatalf("response=%d %s upstream=%d accounting=%+v", response.Code, response.Body.String(), upstreamCalls, capabilities)
	}
}

func TestConservativeInputAdmissionAndUsageObservation(t *testing.T) {
	body := []byte(`{"model":"qualified-model","input":[{"role":"user","content":"hello"}]}`)
	bound, err := conservativeInputTokenUpperBound(body)
	if err != nil || bound <= int64(len(body))+4096 {
		t.Fatalf("conservative input bound=%d err=%v", bound, err)
	}
	sse := newModelUsageObserver("text/event-stream; charset=utf-8")
	sse.Observe([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":42}}}\n\n"))
	if usage, ok := sse.Usage(); !ok || usage != 42 {
		t.Fatalf("SSE usage=%d available=%v", usage, ok)
	}
	jsonObserver := newModelUsageObserver("application/json")
	jsonObserver.Observe([]byte(`{"usage":{"output_tokens":9}}`))
	if usage, ok := jsonObserver.Usage(); !ok || usage != 9 {
		t.Fatalf("JSON usage=%d available=%v", usage, ok)
	}
	missing := newModelUsageObserver("application/json")
	missing.Observe([]byte(`{"usage":{}}`))
	if _, ok := missing.Usage(); ok {
		t.Fatal("missing output token usage was accepted as a zero-token response")
	}
}

func TestRedisModelCapabilityBudgetLedger(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("WORKSFLOW_TEST_REDIS_ADDR is not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connect Redis: %v", err)
	}
	prefix := "test:agent-budget:" + uuid.NewString() + ":"
	authority, err := NewRedisModelCapabilityAuthority(
		client, prefix, "http://agent-model-gateway:8080/internal/agent-model/v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		var cursor uint64
		for {
			keys, next, scanErr := client.Scan(context.Background(), cursor, prefix+"*", 100).Result()
			if scanErr == nil && len(keys) > 0 {
				_ = client.Del(context.Background(), keys...).Err()
			}
			if scanErr != nil || next == 0 {
				break
			}
			cursor = next
		}
	}()
	record := ModelCapabilityRecord{
		SchemaVersion: modelCapabilitySchemaVersion,
		ID:            uuid.NewString(), AttemptID: uuid.NewString(), ProjectID: uuid.NewString(),
		Model: "qualified-model", MaxInputTokens: 10_000, MaxOutputTokens: 150,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if _, err := authority.Acquire(context.Background(), record, time.Minute, 1, 1); !errors.Is(err, ErrModelCapability) {
		t.Fatalf("revoked or absent capability identity was accepted: %v", err)
	}
	if err := client.Set(context.Background(), authority.idKey(record.ID), strings.Repeat("a", 64), time.Hour).Err(); err != nil {
		t.Fatalf("seed live capability identity: %v", err)
	}
	first, err := authority.Acquire(context.Background(), record, time.Minute, 5_000, 100)
	if err != nil || first.OutputTokenAllowance != 100 {
		t.Fatalf("first budget lease=%+v err=%v", first, err)
	}
	if _, err := authority.Acquire(context.Background(), record, time.Minute, 1, 1); !errors.Is(err, ErrModelCapabilityBusy) {
		t.Fatalf("concurrent lease error=%v", err)
	}
	if err := authority.Release(context.Background(), record, first, 20, true); err != nil {
		t.Fatalf("release exact usage: %v", err)
	}
	second, err := authority.Acquire(context.Background(), record, time.Minute, 4_000, 100)
	if err != nil || second.OutputTokenAllowance != 100 {
		t.Fatalf("second budget lease=%+v err=%v", second, err)
	}
	if err := authority.Release(context.Background(), record, first, 20, true); err == nil {
		t.Fatal("stale budget lease released a newer request")
	}
	if err := authority.Release(context.Background(), record, second, 0, false); err != nil {
		t.Fatalf("conservatively charge missing usage: %v", err)
	}
	if _, err := authority.Acquire(context.Background(), record, time.Minute, 2_000, 1); !errors.Is(err, ErrModelInputBudgetExceeded) {
		t.Fatalf("cumulative input budget error=%v", err)
	}
	third, err := authority.Acquire(context.Background(), record, time.Minute, 500, 100)
	if err != nil || third.OutputTokenAllowance != 30 {
		t.Fatalf("remaining output lease=%+v err=%v", third, err)
	}
	if err := authority.Release(context.Background(), record, third, 30, true); err != nil {
		t.Fatalf("release final output allowance: %v", err)
	}
	if _, err := authority.Acquire(context.Background(), record, time.Minute, 1, 1); !errors.Is(err, ErrModelOutputBudgetExceeded) {
		t.Fatalf("cumulative output budget error=%v", err)
	}
}

func TestModelGatewayRejectsInvalidCapabilityAndMutableConfiguration(t *testing.T) {
	capabilities := modelGatewayCapabilitiesFixture()
	if _, err := NewModelGateway(ModelGatewayConfig{
		UpstreamResponsesURL: "https://api.example/v1/responses",
		MaxInputBytes:        1 << 20, MaxOutputBytes: 1 << 20, RequestTimeout: time.Minute,
	}, capabilities, nil, nil); err == nil {
		t.Fatal("Model Gateway accepted a missing upstream platform secret")
	}
	gateway, err := NewModelGateway(ModelGatewayConfig{
		UpstreamResponsesURL: "https://api.example/v1/responses",
		UpstreamAPIKey:       "secret", MaxInputBytes: 1 << 20,
		MaxOutputBytes: 1 << 20, RequestTimeout: time.Minute,
	}, capabilities, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-model/v1/responses", strings.NewReader(`{"model":"qualified-model"}`))
	request.Header.Set("Authorization", "Bearer wrong-token")
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "invalid_model_capability") {
		t.Fatalf("invalid capability response=%d %s", response.Code, response.Body.String())
	}
}

func modelGatewayCapabilitiesFixture() *modelGatewayCapabilitiesFake {
	return &modelGatewayCapabilitiesFake{
		token: "attempt-scoped-short-lived-token",
		record: ModelCapabilityRecord{
			SchemaVersion: modelCapabilitySchemaVersion,
			ID:            uuid.NewString(), AttemptID: uuid.NewString(), ProjectID: uuid.NewString(),
			Model: "qualified-model", MaxInputTokens: 100_000, MaxOutputTokens: 4096,
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
}
