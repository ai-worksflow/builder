package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAIProviderStructuredOutput(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing authorization header")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		text, _ := body["text"].(map[string]any)
		if text["format"] == nil {
			t.Fatal("expected structured output format")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_1","model":"test-model","output_text":"{\"ok\":true}","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()
	provider, err := NewOpenAIProvider(OpenAIConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultModel: "test-model", MaxRetries: 0,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Generate(context.Background(), Request{
		Input:            json.RawMessage(`{"manifest":"pinned"}`),
		OutputSchema:     json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
		OutputSchemaName: "test", MaxOutputTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Output) != `{"ok":true}` || result.Usage == nil || result.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestOpenAIProviderClassifiesRateLimit(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
	}))
	defer server.Close()
	provider, err := NewOpenAIProvider(OpenAIConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultModel: "test-model", MaxRetries: 0,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Generate(context.Background(), Request{
		Input: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit error, got %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("2", now); got != 2*time.Second {
		t.Fatalf("duration=%s", got)
	}
	if got := parseRetryAfter(now.Add(3*time.Second).Format(http.TimeFormat), now); got != 3*time.Second {
		t.Fatalf("date duration=%s", got)
	}
}
