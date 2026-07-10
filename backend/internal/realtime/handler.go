package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/config"
)

type Handler struct {
	hub           *Hub
	authenticator Authenticator
	authorizer    SubscriptionAuthorizer
	history       HistoryReader
	logger        *slog.Logger
	config        config.WebSocketConfig
	upgrader      websocket.Upgrader
}

type clientMessage struct {
	Type           string `json:"type"`
	RequestID      string `json:"requestId,omitempty"`
	BearerToken    string `json:"bearerToken,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	CSRFToken      string `json:"csrfToken,omitempty"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
	Topic          string `json:"topic,omitempty"`
	ProjectID      string `json:"projectId,omitempty"`
	ArtifactID     string `json:"artifactId,omitempty"`
	RunID          string `json:"runId,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
	SentAt         string `json:"sentAt,omitempty"`
}

func NewHandler(
	hub *Hub,
	authenticator Authenticator,
	authorizer SubscriptionAuthorizer,
	logger *slog.Logger,
	cfg config.WebSocketConfig,
	history ...HistoryReader,
) *Handler {
	if authenticator == nil {
		authenticator = RejectAuthenticator{}
	}
	if authorizer == nil {
		authorizer = AllowAllSubscriptions{}
	}
	var historyReader HistoryReader
	if len(history) > 0 {
		historyReader = history[0]
	}
	if cfg.MaxReplayEvents < 1 {
		cfg.MaxReplayEvents = min(50, max(1, cfg.SendBuffer-1))
	}
	handler := &Handler{
		hub: hub, authenticator: authenticator, authorizer: authorizer,
		history: historyReader, logger: logger, config: cfg,
	}
	handler.upgrader = websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: handler.originAllowed,
		Subprotocols: []string{"worksflow.platform.v1"},
	}
	return handler
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !websocket.IsWebSocketUpgrade(request) {
		writeHTTPProblem(writer, request, http.StatusBadRequest, "invalid_websocket_upgrade", "A WebSocket upgrade request is required.")
		return
	}
	if !h.originAllowed(request) {
		writeHTTPProblem(writer, request, http.StatusForbidden, "origin_forbidden", "Origin is not allowed.")
		return
	}
	connection, err := h.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		h.logger.Debug("websocket upgrade rejected", "error", err)
		return
	}
	client, registered := h.hub.registerClient(request.Context())
	if !registered {
		_ = connection.Close()
		return
	}

	disconnected := make(chan struct{})
	go h.writePump(connection, client, disconnected)
	principal, authenticated := h.readPump(request, connection, client)
	<-disconnected
	if authenticated {
		h.logger.Info("websocket disconnected", "principal_id", principal.ID)
	}
}

func writeHTTPProblem(writer http.ResponseWriter, request *http.Request, status int, code, detail string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]interface{}{
		"type": "urn:worksflow:problem:" + code, "title": http.StatusText(status),
		"status": status, "detail": detail, "instance": request.URL.Path,
		"code": code, "requestId": writer.Header().Get("X-Request-ID"),
	})
}

func (h *Handler) readPump(request *http.Request, connection *websocket.Conn, client *client) (Principal, bool) {
	defer func() {
		h.hub.unregisterClient(client)
	}()
	connection.SetReadLimit(h.config.MaxMessageBytes)
	_ = connection.SetReadDeadline(time.Now().Add(h.config.AuthTimeout))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(h.config.PongWait))
	})

	var principal Principal
	authenticated := false
	subscriptions := make(map[string]struct{})
	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			return principal, authenticated
		}
		if messageType != websocket.TextMessage {
			h.sendError(request.Context(), client, "invalid_message", "Only JSON text messages are supported.")
			continue
		}
		var message clientMessage
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&message); err != nil {
			h.sendError(request.Context(), client, "invalid_message", "The WebSocket message is not valid JSON.")
			continue
		}
		var trailing interface{}
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			h.sendError(request.Context(), client, "invalid_message", "The WebSocket message contains multiple JSON values.")
			continue
		}
		if !authenticated {
			if message.Type != "auth" {
				h.sendAuthError(request.Context(), client, "authentication_required", "Authenticate before sending other messages.")
				return principal, false
			}
			principal, err = h.authenticator.Authenticate(request, AuthenticationRequest{
				BearerToken: message.BearerToken, SessionID: message.SessionID, CSRFToken: message.CSRFToken,
			})
			if err != nil || principal.ID == "" {
				h.sendAuthError(request.Context(), client, "authentication_required", "The session is missing, expired, or invalid.")
				return Principal{}, false
			}
			authenticated = true
			_ = connection.SetReadDeadline(time.Now().Add(h.config.PongWait))
			h.sendJSON(request.Context(), client, map[string]interface{}{
				"type": "auth.ack", "connectionId": uuid.NewString(),
				"heartbeatIntervalMs": h.config.PingPeriod.Milliseconds(),
			})
			h.logger.Info("websocket authenticated", "principal_id", principal.ID)
			continue
		}

		switch message.Type {
		case "subscribe":
			subscription := SubscriptionRequest{
				ID: message.SubscriptionID, Topic: message.Topic, ProjectID: message.ProjectID,
				ArtifactID: message.ArtifactID, RunID: message.RunID, Cursor: message.Cursor,
			}
			_, duplicate := subscriptions[subscription.ID]
			if err := validateSubscription(subscription); err != nil || duplicate || len(subscriptions) >= h.config.MaxSubscriptions {
				h.sendError(request.Context(), client, "invalid_subscription", "The subscription request is invalid or exceeds the connection limit.")
				continue
			}
			if err := h.authorizer.AuthorizeSubscription(request, principal, subscription); err != nil {
				h.sendError(request.Context(), client, "subscription_forbidden", "The current user cannot subscribe to this resource.")
				continue
			}
			observedCursor, begun := h.hub.BeginReplaySubscription(request.Context(), client, subscription)
			if !begun {
				h.sendError(request.Context(), client, "invalid_subscription", "The subscription id is already active.")
				continue
			}
			replayed, throughCursor, replayErr := h.replay(request.Context(), subscription, observedCursor)
			if replayErr != nil {
				h.hub.Unsubscribe(request.Context(), client, subscription.ID)
				if errors.Is(replayErr, ErrCursorUnavailable) || errors.Is(replayErr, ErrReplayLimit) {
					h.sendJSON(request.Context(), client, map[string]interface{}{
						"type": "cursor.reset", "subscriptionId": subscription.ID,
						"reason": replayErr.Error(),
					})
				} else {
					h.logger.Warn("realtime history replay failed", "error", replayErr, "subscription_id", subscription.ID)
					h.sendError(request.Context(), client, "history_unavailable", "Realtime history is temporarily unavailable; retry the subscription.")
				}
				continue
			}
			ack := map[string]interface{}{"type": "subscription.ack", "subscriptionId": subscription.ID}
			if throughCursor > 0 {
				ack["cursor"] = strconv.FormatUint(throughCursor, 10)
			}
			frames := make([]Frame, 0, len(replayed)+1)
			if frame, err := jsonFrame(ack); err == nil {
				frames = append(frames, frame)
			} else {
				h.hub.Unsubscribe(request.Context(), client, subscription.ID)
				continue
			}
			for _, event := range replayed {
				frame, err := eventFrame(subscription.ID, event)
				if err != nil {
					h.hub.Unsubscribe(request.Context(), client, subscription.ID)
					frames = nil
					break
				}
				frames = append(frames, frame)
			}
			if frames == nil {
				continue
			}
			if h.hub.CompleteReplay(request.Context(), client, subscription.ID, throughCursor, frames) {
				subscriptions[subscription.ID] = struct{}{}
			} else {
				h.sendJSON(request.Context(), client, map[string]interface{}{
					"type": "cursor.reset", "subscriptionId": subscription.ID,
					"reason": "Live events exceeded the replay buffer.",
				})
			}
		case "unsubscribe":
			if message.SubscriptionID == "" {
				h.sendError(request.Context(), client, "invalid_subscription", "A subscription id is required.")
				continue
			}
			h.hub.Unsubscribe(request.Context(), client, message.SubscriptionID)
			delete(subscriptions, message.SubscriptionID)
		case "heartbeat":
			sentAt := message.SentAt
			if sentAt == "" {
				sentAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			h.sendJSON(request.Context(), client, map[string]interface{}{"type": "heartbeat", "sentAt": sentAt})
		case "heartbeat.ack":
			// Read activity already extended the connection deadline.
		case "auth":
			h.sendError(request.Context(), client, "already_authenticated", "The connection is already authenticated.")
		default:
			h.sendError(request.Context(), client, "unknown_message", "The WebSocket message type is unknown.")
		}
	}
}

func (h *Handler) replay(
	ctx context.Context,
	subscription SubscriptionRequest,
	observedCursor uint64,
) ([]DomainEvent, uint64, error) {
	if subscription.Cursor == "" {
		return []DomainEvent{}, observedCursor, nil
	}
	after, err := strconv.ParseUint(subscription.Cursor, 10, 64)
	if err != nil {
		return nil, 0, ErrCursorUnavailable
	}
	if h.history == nil {
		if after != observedCursor {
			return nil, 0, ErrCursorUnavailable
		}
		return []DomainEvent{}, observedCursor, nil
	}
	first, last, err := h.history.Bounds(ctx)
	if err != nil {
		return nil, 0, err
	}
	if observedCursor > last {
		// The live process observed data that the stream can no longer replay.
		return nil, 0, ErrCursorUnavailable
	}
	if after > last {
		return nil, 0, ErrCursorUnavailable
	}
	if first > 0 && after < first-1 {
		return nil, 0, ErrCursorUnavailable
	}
	events, err := h.history.Replay(ctx, subscription, after, last, h.config.MaxReplayEvents)
	if err != nil {
		return nil, 0, err
	}
	return events, last, nil
}

func (h *Handler) writePump(connection *websocket.Conn, client *client, disconnected chan<- struct{}) {
	defer func() {
		_ = connection.Close()
		close(disconnected)
	}()
	ticker := time.NewTicker(h.config.PingPeriod)
	defer ticker.Stop()
	for {
		select {
		case message, ok := <-client.send:
			_ = connection.SetWriteDeadline(time.Now().Add(h.config.WriteWait))
			if !ok {
				_ = connection.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			messageType := websocket.TextMessage
			if message.Type == FrameBinary {
				messageType = websocket.BinaryMessage
			}
			if err := connection.WriteMessage(messageType, message.Payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = connection.SetWriteDeadline(time.Now().Add(h.config.WriteWait))
			if err := connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (h *Handler) sendJSON(ctx context.Context, client *client, value interface{}) {
	frame, err := jsonFrame(value)
	if err != nil {
		return
	}
	h.hub.Send(ctx, client, frame)
}

func jsonFrame(value interface{}) (Frame, error) {
	payload, err := json.Marshal(value)
	return Frame{Type: FrameText, Payload: payload}, err
}

func (h *Handler) sendAuthError(ctx context.Context, client *client, code, detail string) {
	h.sendJSON(ctx, client, map[string]interface{}{
		"type":    "auth.error",
		"problem": wsProblem(code, http.StatusUnauthorized, "Authentication failed", detail),
	})
}

func (h *Handler) sendError(ctx context.Context, client *client, code, detail string) {
	h.sendJSON(ctx, client, map[string]interface{}{
		"type": "error", "problem": wsProblem(code, http.StatusBadRequest, "WebSocket request failed", detail),
	})
}

func wsProblem(code string, status int, title, detail string) map[string]interface{} {
	return map[string]interface{}{
		"type": "urn:worksflow:problem:" + code, "title": title,
		"status": status, "detail": detail, "code": code,
	}
}

func validateSubscription(subscription SubscriptionRequest) error {
	if subscription.ID == "" || len(subscription.ID) > 256 {
		return ErrUnauthorized
	}
	if _, err := uuid.Parse(subscription.ProjectID); err != nil {
		return err
	}
	switch subscription.Topic {
	case "project":
		if subscription.ArtifactID != "" || subscription.RunID != "" {
			return ErrUnauthorized
		}
	case "artifact":
		if _, err := uuid.Parse(subscription.ArtifactID); err != nil || subscription.RunID != "" {
			return ErrUnauthorized
		}
	case "run":
		if _, err := uuid.Parse(subscription.RunID); err != nil || subscription.ArtifactID != "" {
			return ErrUnauthorized
		}
	default:
		return ErrUnauthorized
	}
	if subscription.Cursor != "" {
		if cursor, err := strconv.ParseUint(subscription.Cursor, 10, 64); err != nil || cursor == 0 {
			return ErrUnauthorized
		}
	}
	return nil
}

func (h *Handler) originAllowed(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	canonical := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
	for _, allowed := range h.config.AllowedOrigins {
		if allowed == "*" || canonical == strings.ToLower(strings.TrimRight(allowed, "/")) {
			return true
		}
	}
	return false
}
