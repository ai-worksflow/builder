package transport

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

type sandboxStreamTicketsFake struct {
	grant  sandbox.ConnectionTicketGrant
	secret string
	origin string
	calls  int
}

func (tickets *sandboxStreamTicketsFake) Consume(
	_ context.Context,
	secret, origin string,
) (sandbox.ConnectionTicketGrant, error) {
	tickets.calls++
	tickets.secret = secret
	tickets.origin = origin
	return tickets.grant, nil
}

type sandboxStreamEventsFake struct {
	mu        sync.Mutex
	sequences map[sandbox.StreamChannel]uint64
	events    map[sandbox.StreamChannel][]sandbox.StreamEnvelope
}

type sandboxStreamTerminalsFake struct {
	calls chan string
}

type sandboxStreamActivityFake struct {
	mu      sync.Mutex
	touches int
	err     error
}

func (activity *sandboxStreamActivityFake) TouchSandboxActivity(
	_ context.Context,
	_ string,
	_ uint64,
) error {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	activity.touches++
	return activity.err
}

func (activity *sandboxStreamActivityFake) count() int {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	return activity.touches
}

func (terminals *sandboxStreamTerminalsFake) AttachTerminal(_ context.Context, input sandbox.TerminalStreamControl) error {
	terminals.calls <- "attach:" + input.TerminalID
	return nil
}
func (terminals *sandboxStreamTerminalsFake) WriteTerminal(_ context.Context, _ sandbox.TerminalStreamControl, value []byte) error {
	terminals.calls <- "input:" + string(value)
	return nil
}
func (terminals *sandboxStreamTerminalsFake) ResizeTerminal(_ context.Context, _ sandbox.TerminalStreamControl, rows, columns uint16) error {
	terminals.calls <- fmt.Sprintf("resize:%dx%d", rows, columns)
	return nil
}
func (terminals *sandboxStreamTerminalsFake) SignalTerminal(_ context.Context, _ sandbox.TerminalStreamControl, signal string) error {
	terminals.calls <- "signal:" + signal
	return nil
}
func (terminals *sandboxStreamTerminalsFake) DetachTerminal(_ context.Context, input sandbox.TerminalStreamControl) error {
	terminals.calls <- "detach:" + input.TerminalID
	return nil
}

func newSandboxStreamEventsFake() *sandboxStreamEventsFake {
	return &sandboxStreamEventsFake{
		sequences: map[sandbox.StreamChannel]uint64{},
		events:    map[sandbox.StreamChannel][]sandbox.StreamEnvelope{},
	}
}

func (events *sandboxStreamEventsFake) Publish(
	_ context.Context,
	input sandbox.StreamEventInput,
) (sandbox.StreamEnvelope, error) {
	events.mu.Lock()
	defer events.mu.Unlock()
	events.sequences[input.Channel]++
	event := sandbox.StreamEnvelope{
		SchemaVersion: sandbox.SandboxStreamSchemaVersion,
		SessionID:     input.SessionID, SessionEpoch: input.SessionEpoch, Channel: input.Channel,
		EventType: input.EventType, Sequence: events.sequences[input.Channel],
		AggregateVersion: input.AggregateVersion, RequestID: input.RequestID,
		CorrelationID: input.CorrelationID, Timestamp: time.Now().UTC(), Payload: input.Payload,
	}
	events.events[input.Channel] = append(events.events[input.Channel], event)
	return event, nil
}

func (events *sandboxStreamEventsFake) Replay(
	_ context.Context,
	_ string,
	_ uint64,
	channel sandbox.StreamChannel,
	after uint64,
	_ int,
) ([]sandbox.StreamEnvelope, uint64, error) {
	events.mu.Lock()
	defer events.mu.Unlock()
	all := events.events[channel]
	result := make([]sandbox.StreamEnvelope, 0, len(all))
	for _, event := range all {
		if event.Sequence > after {
			result = append(result, event)
		}
	}
	return result, events.sequences[channel], nil
}

func (*sandboxStreamEventsFake) ReadAfter(
	ctx context.Context,
	_ string,
	_ uint64,
	_ sandbox.StreamChannel,
	_ uint64,
	_ int,
	_ time.Duration,
) ([]sandbox.StreamEnvelope, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSandboxStreamConsumesOriginBoundTicketAndEmitsSequencedEnvelope(t *testing.T) {
	grant := sandboxStreamGrant()
	tickets := &sandboxStreamTicketsFake{grant: grant}
	events := newSandboxStreamEventsFake()
	activity := &sandboxStreamActivityFake{}
	handler, err := NewSandboxStreamHandler(SandboxStreamDependencies{
		Tickets: tickets, Events: events, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Activity: activity,
		Config:   sandboxStreamTestConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dialer := websocket.Dialer{Subprotocols: []string{sandboxStreamSubprotocol}}
	headers := http.Header{"Origin": []string{"https://builder.example"}}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"?ticket=single-use-secret", headers)
	if err != nil {
		body := ""
		if response != nil && response.Body != nil {
			value, _ := io.ReadAll(response.Body)
			body = string(value)
		}
		t.Fatalf("dial sandbox stream: %v body=%s", err, body)
	}
	defer connection.Close()
	if connection.Subprotocol() != sandboxStreamSubprotocol || tickets.calls != 1 ||
		tickets.secret != "single-use-secret" || tickets.origin != "https://builder.example" ||
		activity.count() != 1 {
		t.Fatalf("ticket/subprotocol mismatch: protocol=%q tickets=%#v", connection.Subprotocol(), tickets)
	}
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event sandbox.StreamEnvelope
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != sandbox.SandboxStreamSchemaVersion || event.SessionID != grant.SessionID ||
		event.SessionEpoch != grant.SessionEpoch || event.Channel != sandbox.ChannelControl ||
		event.EventType != "stream.connected" || event.Sequence != 1 {
		t.Fatalf("invalid first stream envelope: %#v", event)
	}
	command, _ := json.Marshal(map[string]any{
		"schemaVersion": "sandbox-stream-command/v1", "sessionId": grant.SessionID,
		"sessionEpoch": grant.SessionEpoch, "channel": "control", "eventType": "stream.ack", "ack": 1,
	})
	if err := connection.WriteMessage(websocket.TextMessage, command); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxStreamRejectsConnectionWhenActivityFenceIsUnavailable(t *testing.T) {
	tickets := &sandboxStreamTicketsFake{grant: sandboxStreamGrant()}
	handler, err := NewSandboxStreamHandler(SandboxStreamDependencies{
		Tickets: tickets, Events: newSandboxStreamEventsFake(),
		Activity: &sandboxStreamActivityFake{err: sandbox.ErrEpochFenced},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)), Config: sandboxStreamTestConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	dialer := websocket.Dialer{Subprotocols: []string{sandboxStreamSubprotocol}}
	_, response, err := dialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"?ticket=single-use-secret",
		http.Header{"Origin": []string{"https://builder.example"}},
	)
	if err == nil || response == nil || response.StatusCode != http.StatusConflict || tickets.calls != 1 {
		t.Fatalf("activity-fenced stream was accepted: response=%#v err=%v tickets=%d", response, err, tickets.calls)
	}
}

func TestSandboxStreamRejectsOriginAndSubprotocolBeforeConsumingTicket(t *testing.T) {
	for _, test := range []struct {
		name        string
		origin      string
		subprotocol string
		wantStatus  int
	}{
		{name: "origin", origin: "https://attacker.example", subprotocol: sandboxStreamSubprotocol, wantStatus: http.StatusForbidden},
		{name: "subprotocol", origin: "https://builder.example", subprotocol: "other.v1", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			tickets := &sandboxStreamTicketsFake{grant: sandboxStreamGrant()}
			handler, err := NewSandboxStreamHandler(SandboxStreamDependencies{
				Tickets: tickets, Events: newSandboxStreamEventsFake(),
				Activity: &sandboxStreamActivityFake{},
				Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)), Config: sandboxStreamTestConfig(),
			})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			dialer := websocket.Dialer{Subprotocols: []string{test.subprotocol}}
			_, response, err := dialer.Dial(
				"ws"+strings.TrimPrefix(server.URL, "http")+"?ticket=unused",
				http.Header{"Origin": []string{test.origin}},
			)
			if err == nil || response == nil || response.StatusCode != test.wantStatus || tickets.calls != 0 {
				t.Fatalf("unsafe handshake: response=%#v err=%v ticketCalls=%d", response, err, tickets.calls)
			}
		})
	}
}

func TestSandboxStreamUsesTypedPTYBinaryFramesAndExactTicketScope(t *testing.T) {
	grant := sandboxStreamGrant()
	grant.Channels = []sandbox.StreamChannel{sandbox.ChannelPTY}
	grant.Cursors = []sandbox.ConnectionCursor{{Channel: sandbox.ChannelPTY}}
	tickets := &sandboxStreamTicketsFake{grant: grant}
	events := newSandboxStreamEventsFake()
	terminalID := "33333333-3333-4333-8333-333333333333"
	payload, _ := json.Marshal(map[string]string{
		"terminalId":  terminalID,
		"valueBase64": base64.RawStdEncoding.EncodeToString([]byte("ready\r\n")),
	})
	if _, err := events.Publish(context.Background(), sandbox.StreamEventInput{
		SessionID: grant.SessionID, SessionEpoch: grant.SessionEpoch, Channel: sandbox.ChannelPTY,
		EventType: "pty.output", RequestID: terminalID, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	terminals := &sandboxStreamTerminalsFake{calls: make(chan string, 8)}
	handler, err := NewSandboxStreamHandler(SandboxStreamDependencies{
		Tickets: tickets, Events: events, Terminals: terminals,
		Activity: &sandboxStreamActivityFake{},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)), Config: sandboxStreamTestConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dialer := websocket.Dialer{Subprotocols: []string{sandboxStreamSubprotocol}}
	connection, _, err := dialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"?ticket=single-use-secret",
		http.Header{"Origin": []string{"https://builder.example"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	messageType, value, err := connection.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || len(value) != sandbox.PTYBinaryHeaderSize+7 ||
		string(value[:4]) != "WFPT" || value[5] != byte(sandbox.PTYFrameOutput) ||
		binary.BigEndian.Uint64(value[8:16]) != grant.SessionEpoch ||
		binary.BigEndian.Uint64(value[16:24]) != 1 || string(value[sandbox.PTYBinaryHeaderSize:]) != "ready\r\n" {
		t.Fatalf("unexpected server PTY frame: type=%d value=%x err=%v", messageType, value, err)
	}
	requestID := "44444444-4444-4444-8444-444444444444"
	attach, err := sandbox.EncodePTYBinaryFrame(sandbox.PTYBinaryFrame{
		Type: sandbox.PTYFrameAttach, SessionEpoch: grant.SessionEpoch, Sequence: 1, Ack: 1,
		TerminalID: terminalID, RequestID: requestID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, attach); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-terminals.calls:
		if call != "attach:"+terminalID {
			t.Fatalf("unexpected terminal call: %s", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal attach was not delivered")
	}
	input, err := sandbox.EncodePTYBinaryFrame(sandbox.PTYBinaryFrame{
		Type: sandbox.PTYFrameInput, SessionEpoch: grant.SessionEpoch, Sequence: 2, Ack: 1,
		TerminalID: terminalID, RequestID: "55555555-5555-4555-8555-555555555555",
		Payload: []byte("pwd\r"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, input); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-terminals.calls:
		if call != "input:pwd\r" {
			t.Fatalf("unexpected terminal call: %s", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal input was not delivered")
	}
}

func sandboxStreamGrant() sandbox.ConnectionTicketGrant {
	return sandbox.ConnectionTicketGrant{
		SchemaVersion: sandbox.ConnectionTicketSchemaVersion,
		ID:            sandboxTransportCandidate, ProjectID: sandboxTransportProject,
		SessionID: sandboxTransportSession, ActorID: sandboxTransportActor,
		SessionEpoch: 2, Origin: "https://builder.example",
		Channels: []sandbox.StreamChannel{sandbox.ChannelControl},
		Cursors:  []sandbox.ConnectionCursor{{Channel: sandbox.ChannelControl}},
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
}

func sandboxStreamTestConfig() config.WebSocketConfig {
	return config.WebSocketConfig{
		AllowedOrigins: []string{"https://builder.example"},
		WriteWait:      time.Second, PongWait: 4 * time.Second, PingPeriod: 3 * time.Second,
		MaxMessageBytes: 64 << 10, SendBuffer: 16, MaxReplayEvents: 8,
	}
}

var _ sandbox.StreamEventStore = (*sandboxStreamEventsFake)(nil)
var _ SandboxConnectionTicketConsumer = (*sandboxStreamTicketsFake)(nil)
var _ sandbox.TerminalStreamController = (*sandboxStreamTerminalsFake)(nil)
