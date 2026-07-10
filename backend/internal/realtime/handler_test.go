package realtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/config"
)

func TestHandlerAuthenticatesSubscribesAndDeliversEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := NewHub(8)
	go hub.Run(ctx)

	handler := NewHandler(hub, AnonymousAuthenticator{}, AllowAllSubscriptions{}, testLogger(), testWebSocketConfig())
	server := httptest.NewServer(handler)
	defer server.Close()
	connection := dialTestSocket(t, server.URL, "https://app.example.com")
	defer connection.Close()

	writeSocketJSON(t, connection, map[string]interface{}{"type": "auth", "requestId": "req-1"})
	if message := readSocketJSON(t, connection); message["type"] != "auth.ack" {
		t.Fatalf("auth response = %#v", message)
	}
	projectID := uuid.NewString()
	writeSocketJSON(t, connection, map[string]interface{}{
		"type": "subscribe", "requestId": "req-2", "subscriptionId": "project:" + projectID,
		"topic": "project", "projectId": projectID,
	})
	if message := readSocketJSON(t, connection); message["type"] != "subscription.ack" {
		t.Fatalf("subscription response = %#v", message)
	}
	if !hub.Publish(DomainEvent{
		ID: uuid.NewString(), Type: "project.updated", Cursor: 7, ProjectID: projectID,
		OccurredAt: time.Now(), Payload: json.RawMessage(`{"id":"project"}`),
	}) {
		t.Fatal("Publish() = false")
	}
	message := readSocketJSON(t, connection)
	if message["type"] != "event" {
		t.Fatalf("event response = %#v", message)
	}
	event, ok := message["event"].(map[string]interface{})
	if !ok || event["cursor"] != "7" || event["subscriptionId"] != "project:"+projectID {
		t.Fatalf("event = %#v", message["event"])
	}
}

func TestHandlerReplaysHistoryBeforeBufferedLiveEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := NewHub(8)
	go hub.Run(ctx)
	projectID := uuid.NewString()
	replayStarted := make(chan struct{})
	releaseReplay := make(chan struct{})
	history := &stubHistoryReader{
		first: 4, last: 7,
		events: []DomainEvent{
			{ID: "event-6", Type: "artifact.updated", Cursor: 6, ProjectID: projectID, OccurredAt: time.Now(), Payload: json.RawMessage(`{"order":6}`)},
			{ID: "event-7", Type: "artifact.updated", Cursor: 7, ProjectID: projectID, OccurredAt: time.Now(), Payload: json.RawMessage(`{"order":7}`)},
		},
		replayStarted: replayStarted,
		releaseReplay: releaseReplay,
	}
	cfg := testWebSocketConfig()
	cfg.MaxReplayEvents = 7
	handler := NewHandler(hub, AnonymousAuthenticator{}, AllowAllSubscriptions{}, testLogger(), cfg, history)
	server := httptest.NewServer(handler)
	defer server.Close()
	connection := dialTestSocket(t, server.URL, "https://app.example.com")
	defer connection.Close()

	writeSocketJSON(t, connection, map[string]interface{}{"type": "auth"})
	_ = readSocketJSON(t, connection)
	writeSocketJSON(t, connection, map[string]interface{}{
		"type": "subscribe", "subscriptionId": "project:" + projectID,
		"topic": "project", "projectId": projectID, "cursor": "5",
	})
	select {
	case <-replayStarted:
	case <-time.After(time.Second):
		t.Fatal("history replay did not start")
	}
	if !hub.Publish(DomainEvent{
		ID: "event-8", Type: "artifact.updated", Cursor: 8, ProjectID: projectID,
		OccurredAt: time.Now(), Payload: json.RawMessage(`{"order":8}`),
	}) {
		t.Fatal("Publish() = false")
	}
	deadline := time.Now().Add(time.Second)
	for hub.Cursor() != "8" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.Cursor() != "8" {
		t.Fatal("live event was not observed while replay was paused")
	}
	close(releaseReplay)

	if message := readSocketJSON(t, connection); message["type"] != "subscription.ack" || message["cursor"] != "7" {
		t.Fatalf("subscription acknowledgement = %#v", message)
	}
	for _, expected := range []string{"6", "7", "8"} {
		message := readSocketJSON(t, connection)
		event, _ := message["event"].(map[string]interface{})
		if message["type"] != "event" || event["cursor"] != expected {
			t.Fatalf("event = %#v, want cursor %s", message, expected)
		}
	}
}

func TestHandlerResetsExpiredCursorWithoutActivatingSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := NewHub(8)
	go hub.Run(ctx)
	history := &stubHistoryReader{first: 10, last: 12}
	cfg := testWebSocketConfig()
	cfg.MaxReplayEvents = 7
	handler := NewHandler(hub, AnonymousAuthenticator{}, AllowAllSubscriptions{}, testLogger(), cfg, history)
	server := httptest.NewServer(handler)
	defer server.Close()
	connection := dialTestSocket(t, server.URL, "https://app.example.com")
	defer connection.Close()

	writeSocketJSON(t, connection, map[string]interface{}{"type": "auth"})
	_ = readSocketJSON(t, connection)
	writeSocketJSON(t, connection, map[string]interface{}{
		"type": "subscribe", "subscriptionId": "project:expired",
		"topic": "project", "projectId": uuid.NewString(), "cursor": "2",
	})
	message := readSocketJSON(t, connection)
	if message["type"] != "cursor.reset" || message["subscriptionId"] != "project:expired" {
		t.Fatalf("cursor response = %#v", message)
	}
}

func TestHandlerReturnsAuthenticationError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := NewHub(1)
	go hub.Run(ctx)
	handler := NewHandler(hub, RejectAuthenticator{}, AllowAllSubscriptions{}, testLogger(), testWebSocketConfig())
	server := httptest.NewServer(handler)
	defer server.Close()
	connection := dialTestSocket(t, server.URL, "https://app.example.com")
	defer connection.Close()
	writeSocketJSON(t, connection, map[string]interface{}{"type": "auth", "requestId": "req"})
	if message := readSocketJSON(t, connection); message["type"] != "auth.error" {
		t.Fatalf("auth response = %#v", message)
	}
}

func TestHandlerRejectsUnexpectedOrigin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := NewHub(1)
	go hub.Run(ctx)
	handler := NewHandler(hub, AnonymousAuthenticator{}, AllowAllSubscriptions{}, testLogger(), testWebSocketConfig())
	server := httptest.NewServer(handler)
	defer server.Close()

	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http")
	headers := http.Header{"Origin": []string{"https://evil.example.com"}}
	connection, response, err := websocket.DefaultDialer.Dial(websocketURL, headers)
	if connection != nil {
		_ = connection.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("Dial() error = %v, status = %d, want forbidden upgrade", err, status)
	}
}

func TestHandlerReturnsProblemForNonUpgradeRequest(t *testing.T) {
	handler := NewHandler(NewHub(1), RejectAuthenticator{}, AllowAllSubscriptions{}, testLogger(), testWebSocketConfig())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/ws", nil))
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response = %d %#v %s", response.Code, response.Header(), response.Body.String())
	}
}

func dialTestSocket(t *testing.T, serverURL, origin string) *websocket.Conn {
	t.Helper()
	websocketURL := "ws" + strings.TrimPrefix(serverURL, "http")
	headers := http.Header{"Origin": []string{origin}}
	connection, response, err := websocket.DefaultDialer.Dial(websocketURL, headers)
	if err != nil {
		if response != nil {
			t.Fatalf("Dial() error = %v, status = %d", err, response.StatusCode)
		}
		t.Fatalf("Dial() error = %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	return connection
}

func writeSocketJSON(t *testing.T, connection *websocket.Conn, value interface{}) {
	t.Helper()
	if err := connection.WriteJSON(value); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
}

func readSocketJSON(t *testing.T, connection *websocket.Conn) map[string]interface{} {
	t.Helper()
	var value map[string]interface{}
	if err := connection.ReadJSON(&value); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	return value
}

func testWebSocketConfig() config.WebSocketConfig {
	return config.WebSocketConfig{
		AllowedOrigins:   []string{"https://app.example.com"},
		AuthTimeout:      time.Second,
		WriteWait:        time.Second,
		PongWait:         5 * time.Second,
		PingPeriod:       4 * time.Second,
		MaxMessageBytes:  1024,
		SendBuffer:       8,
		MaxSubscriptions: 8,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type stubHistoryReader struct {
	first         uint64
	last          uint64
	events        []DomainEvent
	err           error
	replayStarted chan struct{}
	releaseReplay chan struct{}
}

func (s *stubHistoryReader) Bounds(context.Context) (uint64, uint64, error) {
	return s.first, s.last, s.err
}

func (s *stubHistoryReader) Replay(
	ctx context.Context,
	_ SubscriptionRequest,
	_, _ uint64,
	_ int,
) ([]DomainEvent, error) {
	if s.replayStarted != nil {
		close(s.replayStarted)
	}
	if s.releaseReplay != nil {
		select {
		case <-s.releaseReplay:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return append([]DomainEvent(nil), s.events...), s.err
}
