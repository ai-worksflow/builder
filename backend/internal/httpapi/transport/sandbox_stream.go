package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/httpapi/requestorigin"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

const sandboxStreamSubprotocol = "worksflow.sandbox.v1"

type SandboxConnectionTicketConsumer interface {
	Consume(context.Context, string, string) (sandbox.ConnectionTicketGrant, error)
}

type SandboxStreamDependencies struct {
	Tickets   SandboxConnectionTicketConsumer
	Events    sandbox.StreamEventStore
	Terminals sandbox.TerminalStreamController
	Activity  sandbox.SandboxActivityRecorder
	Logger    *slog.Logger
	Config    config.WebSocketConfig
}

type SandboxStreamHandler struct {
	tickets   SandboxConnectionTicketConsumer
	events    sandbox.StreamEventStore
	terminals sandbox.TerminalStreamController
	activity  sandbox.SandboxActivityRecorder
	logger    *slog.Logger
	config    config.WebSocketConfig
	upgrader  websocket.Upgrader
}

func NewSandboxStreamHandler(dependencies SandboxStreamDependencies) (*SandboxStreamHandler, error) {
	if dependencies.Tickets == nil || dependencies.Events == nil || dependencies.Activity == nil || dependencies.Logger == nil {
		return nil, errors.New("sandbox stream tickets, events, activity, and logger are required")
	}
	cfg := dependencies.Config
	if cfg.WriteWait <= 0 || cfg.PongWait <= 0 || cfg.PingPeriod <= 0 || cfg.PingPeriod >= cfg.PongWait ||
		cfg.MaxMessageBytes < 1 || cfg.SendBuffer < 1 || cfg.MaxReplayEvents < 1 {
		return nil, errors.New("sandbox stream WebSocket configuration is invalid")
	}
	handler := &SandboxStreamHandler{
		tickets: dependencies.Tickets, events: dependencies.Events, terminals: dependencies.Terminals,
		activity: dependencies.Activity, logger: dependencies.Logger, config: cfg,
	}
	handler.upgrader = websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096,
		Subprotocols: []string{sandboxStreamSubprotocol}, CheckOrigin: handler.originAllowed,
	}
	return handler, nil
}

func (handler *SandboxStreamHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request == nil || !websocket.IsWebSocketUpgrade(request) {
		writeSandboxStreamHTTPProblem(writer, request, http.StatusBadRequest, "invalid_websocket_upgrade", "A WebSocket upgrade request is required.")
		return
	}
	if !handler.originAllowed(request) {
		writeSandboxStreamHTTPProblem(writer, request, http.StatusForbidden, "origin_forbidden", "The browser Origin is not allowed.")
		return
	}
	if !websocketSubprotocolRequested(request, sandboxStreamSubprotocol) {
		writeSandboxStreamHTTPProblem(writer, request, http.StatusBadRequest, "sandbox_stream_subprotocol_required", "The worksflow.sandbox.v1 WebSocket subprotocol is required.")
		return
	}
	ticketValues := request.URL.Query()["ticket"]
	if len(ticketValues) != 1 || strings.TrimSpace(ticketValues[0]) == "" {
		writeSandboxStreamHTTPProblem(writer, request, http.StatusUnauthorized, "sandbox_connection_ticket_required", "A single-use sandbox connection ticket is required.")
		return
	}
	origin := request.Header.Get("Origin")
	grant, err := handler.tickets.Consume(request.Context(), ticketValues[0], origin)
	if err != nil {
		status, code := http.StatusUnauthorized, "sandbox_connection_ticket_rejected"
		if errors.Is(err, sandbox.ErrConnectionTicketUnavailable) {
			status, code = http.StatusServiceUnavailable, "sandbox_connection_ticket_unavailable"
		} else if errors.Is(err, sandbox.ErrEpochFenced) || errors.Is(err, sandbox.ErrActionBlocked) {
			status, code = http.StatusConflict, "sandbox_stream_fenced"
		}
		writeSandboxStreamHTTPProblem(writer, request, status, code, "The ticket was consumed but no longer authorizes this session epoch and channel scope.")
		return
	}
	if err := handler.activity.TouchSandboxActivity(request.Context(), grant.SessionID, grant.SessionEpoch); err != nil {
		status, code := http.StatusConflict, "sandbox_stream_activity_fenced"
		if errors.Is(err, sandbox.ErrDeadlineStoreUnavailable) {
			status, code = http.StatusServiceUnavailable, "sandbox_stream_activity_unavailable"
		}
		writeSandboxStreamHTTPProblem(writer, request, status, code, "The sandbox activity fence rejected this connection.")
		return
	}
	connection, err := handler.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		handler.logger.Debug("sandbox WebSocket upgrade rejected", "error", err, "session_id", grant.SessionID)
		return
	}
	handler.serveConnection(request.Context(), connection, grant)
}

func (handler *SandboxStreamHandler) serveConnection(
	parent context.Context,
	connection *websocket.Conn,
	grant sandbox.ConnectionTicketGrant,
) {
	defer connection.Close()
	connection.SetReadLimit(handler.config.MaxMessageBytes)
	_ = connection.SetReadDeadline(time.Now().Add(handler.config.PongWait))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(handler.config.PongWait))
	})

	connectionID := uuid.NewString()
	if connectionChannelAllowed(grant, sandbox.ChannelControl) {
		payload, _ := json.Marshal(map[string]any{
			"connectionId":        connectionID,
			"heartbeatIntervalMs": handler.config.PingPeriod.Milliseconds(),
		})
		if _, err := handler.events.Publish(parent, sandbox.StreamEventInput{
			SessionID: grant.SessionID, SessionEpoch: grant.SessionEpoch, Channel: sandbox.ChannelControl,
			EventType: "stream.connected", RequestID: grant.ID, Payload: payload,
		}); err != nil {
			handler.closeStream(connection, websocket.CloseInternalServerErr, "stream unavailable")
			return
		}
	}

	after := make(map[sandbox.StreamChannel]uint64, len(grant.Cursors))
	for _, cursor := range grant.Cursors {
		events, through, err := handler.events.Replay(
			parent, grant.SessionID, grant.SessionEpoch, cursor.Channel,
			cursor.LastAckedSeq, handler.config.MaxReplayEvents,
		)
		if errors.Is(err, sandbox.ErrStreamCursorUnavailable) || errors.Is(err, sandbox.ErrStreamReplayLimit) {
			payload, _ := json.Marshal(map[string]any{
				"channel": cursor.Channel, "reason": "cursor_outside_retained_window",
				"sessionPath": "/v1/sandbox-sessions/" + grant.SessionID,
				"treePath":    "/v1/sandbox-sessions/" + grant.SessionID + "/tree",
			})
			reset, publishErr := handler.events.Publish(parent, sandbox.StreamEventInput{
				SessionID: grant.SessionID, SessionEpoch: grant.SessionEpoch, Channel: cursor.Channel,
				EventType: "stream.reset", RequestID: grant.ID, Payload: payload,
			})
			if publishErr != nil || handler.writeInitialEvent(connection, reset) != nil {
				handler.closeStream(connection, websocket.CloseInternalServerErr, "stream unavailable")
				return
			}
			after[cursor.Channel] = reset.Sequence
			continue
		}
		if err != nil {
			handler.closeStream(connection, websocket.CloseInternalServerErr, "stream unavailable")
			return
		}
		for _, event := range events {
			if err := handler.writeInitialEvent(connection, event); err != nil {
				return
			}
		}
		after[cursor.Channel] = through
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ptyState := newPTYStreamState()
	outgoing := make(chan sandbox.StreamEnvelope, handler.config.SendBuffer)
	failures := make(chan error, 1)
	var readers sync.WaitGroup
	for channel, sequence := range after {
		readers.Add(1)
		go func(channel sandbox.StreamChannel, sequence uint64) {
			defer readers.Done()
			handler.readLive(ctx, grant, channel, sequence, outgoing, failures)
		}(channel, sequence)
	}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		handler.writeLive(ctx, connection, outgoing, failures, ptyState)
	}()

	handler.readCommands(ctx, connection, grant, ptyState)
	handler.detachTerminals(grant, ptyState)
	cancel()
	readers.Wait()
	<-writerDone
	handler.logger.Info("sandbox WebSocket disconnected", "session_id", grant.SessionID, "actor_id", grant.ActorID, "connection_id", connectionID)
}

func (handler *SandboxStreamHandler) readLive(
	ctx context.Context,
	grant sandbox.ConnectionTicketGrant,
	channel sandbox.StreamChannel,
	after uint64,
	outgoing chan<- sandbox.StreamEnvelope,
	failures chan<- error,
) {
	block := min(handler.config.PingPeriod, 5*time.Second)
	for ctx.Err() == nil {
		events, err := handler.events.ReadAfter(
			ctx, grant.SessionID, grant.SessionEpoch, channel, after,
			handler.config.MaxReplayEvents, block,
		)
		if err != nil {
			if ctx.Err() == nil {
				select {
				case failures <- err:
				default:
				}
			}
			return
		}
		for _, event := range events {
			select {
			case outgoing <- event:
				after = event.Sequence
			case <-ctx.Done():
				return
			default:
				select {
				case failures <- errors.New("sandbox stream client is too slow"):
				default:
				}
				return
			}
		}
	}
}

func (handler *SandboxStreamHandler) writeLive(
	ctx context.Context,
	connection *websocket.Conn,
	outgoing <-chan sandbox.StreamEnvelope,
	failures <-chan error,
	ptyState *ptyStreamState,
) {
	ticker := time.NewTicker(handler.config.PingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			handler.closeStream(connection, websocket.CloseNormalClosure, "connection closed")
			return
		case event := <-outgoing:
			_ = connection.SetWriteDeadline(time.Now().Add(handler.config.WriteWait))
			if err := handler.writeEvent(connection, event, ptyState.ackFor(event)); err != nil {
				return
			}
		case err := <-failures:
			handler.logger.Warn("sandbox stream reader stopped", "error", err)
			handler.closeStream(connection, websocket.CloseInternalServerErr, "stream unavailable")
			return
		case <-ticker.C:
			_ = connection.SetWriteDeadline(time.Now().Add(handler.config.WriteWait))
			if err := connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

type sandboxStreamCommand struct {
	SchemaVersion string                `json:"schemaVersion"`
	SessionID     string                `json:"sessionId"`
	SessionEpoch  uint64                `json:"sessionEpoch"`
	Channel       sandbox.StreamChannel `json:"channel"`
	EventType     string                `json:"eventType"`
	Ack           uint64                `json:"ack"`
	RequestID     string                `json:"requestId,omitempty"`
}

type ptyStreamState struct {
	mu        sync.RWMutex
	sequences map[string]uint64
	acks      map[string]uint64
	attached  map[string]sandbox.TerminalStreamControl
}

func newPTYStreamState() *ptyStreamState {
	return &ptyStreamState{
		sequences: map[string]uint64{}, acks: map[string]uint64{},
		attached: map[string]sandbox.TerminalStreamControl{},
	}
}

func (state *ptyStreamState) accept(frame sandbox.PTYBinaryFrame) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if frame.Sequence != state.sequences[frame.TerminalID]+1 || frame.Ack < state.acks[frame.TerminalID] {
		return false
	}
	state.sequences[frame.TerminalID] = frame.Sequence
	state.acks[frame.TerminalID] = frame.Ack
	return true
}

func (state *ptyStreamState) isAttached(terminalID string) bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	_, exists := state.attached[terminalID]
	return exists
}

func (state *ptyStreamState) attach(control sandbox.TerminalStreamControl) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.attached[control.TerminalID] = control
}

func (state *ptyStreamState) detach(terminalID string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	delete(state.attached, terminalID)
}

func (state *ptyStreamState) attachments() []sandbox.TerminalStreamControl {
	state.mu.RLock()
	defer state.mu.RUnlock()
	result := make([]sandbox.TerminalStreamControl, 0, len(state.attached))
	for _, control := range state.attached {
		result = append(result, control)
	}
	return result
}

func (state *ptyStreamState) ackFor(event sandbox.StreamEnvelope) uint64 {
	if event.Channel != sandbox.ChannelPTY || event.EventType != "pty.output" {
		return 0
	}
	var payload struct {
		TerminalID string `json:"terminalId"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil {
		return 0
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.sequences[payload.TerminalID]
}

func (handler *SandboxStreamHandler) readCommands(
	ctx context.Context,
	connection *websocket.Conn,
	grant sandbox.ConnectionTicketGrant,
	ptyState *ptyStreamState,
) {
	acks := make(map[sandbox.StreamChannel]uint64, len(grant.Channels))
	lastHeartbeat := time.Time{}
	for ctx.Err() == nil {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			return
		}
		if messageType == websocket.BinaryMessage {
			if !connectionChannelAllowed(grant, sandbox.ChannelPTY) || handler.terminals == nil {
				handler.closeStream(connection, websocket.ClosePolicyViolation, "PTY channel is unavailable")
				return
			}
			frame, decodeErr := sandbox.DecodePTYClientFrame(payload)
			if decodeErr != nil || frame.SessionEpoch != grant.SessionEpoch || !ptyState.accept(frame) {
				handler.closeStream(connection, websocket.ClosePolicyViolation, "PTY binary frame is fenced")
				return
			}
			if controlErr := handler.handlePTYFrame(ctx, grant, ptyState, frame); controlErr != nil {
				handler.closeStream(connection, websocket.ClosePolicyViolation, "PTY command was rejected")
				return
			}
			continue
		}
		if messageType != websocket.TextMessage {
			handler.closeStream(connection, websocket.CloseUnsupportedData, "JSON commands or typed PTY binary frames are required")
			return
		}
		var command sandboxStreamCommand
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&command); err != nil {
			handler.closeStream(connection, websocket.CloseInvalidFramePayloadData, "invalid stream command")
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
			command.SchemaVersion != "sandbox-stream-command/v1" || command.SessionID != grant.SessionID ||
			command.SessionEpoch != grant.SessionEpoch || !connectionChannelAllowed(grant, command.Channel) {
			handler.closeStream(connection, websocket.ClosePolicyViolation, "stream command is fenced")
			return
		}
		switch command.EventType {
		case "stream.ack":
			if command.Ack < acks[command.Channel] {
				handler.closeStream(connection, websocket.ClosePolicyViolation, "stream acknowledgement moved backwards")
				return
			}
			acks[command.Channel] = command.Ack
		case "stream.heartbeat":
			if command.Channel != sandbox.ChannelControl || (!lastHeartbeat.IsZero() && time.Since(lastHeartbeat) < time.Second) {
				continue
			}
			lastHeartbeat = time.Now()
			if err := handler.activity.TouchSandboxActivity(ctx, grant.SessionID, grant.SessionEpoch); err != nil {
				handler.closeStream(connection, websocket.ClosePolicyViolation, "sandbox activity was fenced")
				return
			}
			response, _ := json.Marshal(map[string]any{"ack": command.Ack})
			_, _ = handler.events.Publish(ctx, sandbox.StreamEventInput{
				SessionID: grant.SessionID, SessionEpoch: grant.SessionEpoch, Channel: sandbox.ChannelControl,
				EventType: "stream.heartbeat", RequestID: command.RequestID, Payload: response,
			})
		default:
			handler.closeStream(connection, websocket.CloseUnsupportedData, "stream command is not supported")
			return
		}
	}
}

func (handler *SandboxStreamHandler) handlePTYFrame(
	ctx context.Context,
	grant sandbox.ConnectionTicketGrant,
	state *ptyStreamState,
	frame sandbox.PTYBinaryFrame,
) error {
	if err := handler.activity.TouchSandboxActivity(ctx, grant.SessionID, grant.SessionEpoch); err != nil {
		return err
	}
	control := sandbox.TerminalStreamControl{
		ProjectID: grant.ProjectID, SessionID: grant.SessionID, SessionEpoch: grant.SessionEpoch,
		ActorID: grant.ActorID, TerminalID: frame.TerminalID, RequestID: frame.RequestID,
	}
	if frame.Type == sandbox.PTYFrameAttach {
		if state.isAttached(frame.TerminalID) {
			return sandbox.ErrTerminalInvalidTransition
		}
		if err := handler.terminals.AttachTerminal(ctx, control); err != nil {
			return err
		}
		state.attach(control)
		return nil
	}
	if !state.isAttached(frame.TerminalID) {
		return sandbox.ErrTerminalInvalidTransition
	}
	switch frame.Type {
	case sandbox.PTYFrameInput:
		return handler.terminals.WriteTerminal(ctx, control, frame.Payload)
	case sandbox.PTYFrameResize:
		rows := uint16(frame.Payload[0])<<8 | uint16(frame.Payload[1])
		columns := uint16(frame.Payload[2])<<8 | uint16(frame.Payload[3])
		return handler.terminals.ResizeTerminal(ctx, control, rows, columns)
	case sandbox.PTYFrameSignal:
		return handler.terminals.SignalTerminal(ctx, control, string(frame.Payload))
	case sandbox.PTYFrameDetach:
		if err := handler.terminals.DetachTerminal(ctx, control); err != nil {
			return err
		}
		state.detach(frame.TerminalID)
		return nil
	default:
		return sandbox.ErrTerminalInvalid
	}
}

func (handler *SandboxStreamHandler) detachTerminals(
	grant sandbox.ConnectionTicketGrant,
	state *ptyStreamState,
) {
	if handler.terminals == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, control := range state.attachments() {
		control.RequestID = uuid.NewString()
		_ = handler.terminals.DetachTerminal(ctx, control)
	}
}

func (handler *SandboxStreamHandler) writeInitialEvent(connection *websocket.Conn, event sandbox.StreamEnvelope) error {
	_ = connection.SetWriteDeadline(time.Now().Add(handler.config.WriteWait))
	return handler.writeEvent(connection, event, 0)
}

func (handler *SandboxStreamHandler) writeEvent(
	connection *websocket.Conn,
	event sandbox.StreamEnvelope,
	ack uint64,
) error {
	if event.Channel == sandbox.ChannelPTY && event.EventType == "pty.output" {
		frame, err := sandbox.EncodePTYServerOutputFrame(event, ack)
		if err != nil {
			return err
		}
		return connection.WriteMessage(websocket.BinaryMessage, frame)
	}
	return connection.WriteJSON(event)
}

func (handler *SandboxStreamHandler) closeStream(connection *websocket.Conn, code int, reason string) {
	_ = connection.SetWriteDeadline(time.Now().Add(handler.config.WriteWait))
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(handler.config.WriteWait))
}

func (handler *SandboxStreamHandler) originAllowed(request *http.Request) bool {
	if request == nil {
		return false
	}
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	if requestorigin.Same(request, origin) {
		return true
	}
	for _, allowed := range handler.config.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(strings.TrimRight(strings.TrimSpace(allowed), "/"), strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func websocketSubprotocolRequested(request *http.Request, expected string) bool {
	for _, value := range strings.Split(request.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}

func connectionChannelAllowed(grant sandbox.ConnectionTicketGrant, channel sandbox.StreamChannel) bool {
	for _, allowed := range grant.Channels {
		if allowed == channel {
			return true
		}
	}
	return false
}

func writeSandboxStreamHTTPProblem(
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
		instance = request.URL.Path
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"type": "urn:worksflow:problem:" + code, "title": http.StatusText(status),
		"status": status, "detail": detail, "instance": instance, "code": code,
		"requestId": writer.Header().Get("X-Request-ID"),
	})
}

var _ http.Handler = (*SandboxStreamHandler)(nil)
