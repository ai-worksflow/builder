package transport

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/lsp"
)

var errLSPGatewayFrame = errors.New("invalid or closed LSP Gateway WebSocket frame")

// LSPGatewayCore is the transport-independent bound Gateway implemented by
// lsp.Gateway. The HTTP layer retains ownership of WebSocket close codes and
// the physical socket lifecycle.
type LSPGatewayCore interface {
	Serve(context.Context, lsp.FrameConnection, lsp.TicketGrant, lsp.ClientBind) error
}

type WebSocketLSPGateway struct {
	core      LSPGatewayCore
	writeWait time.Duration
}

func NewWebSocketLSPGateway(core LSPGatewayCore, writeWait time.Duration) (*WebSocketLSPGateway, error) {
	if core == nil || writeWait < time.Millisecond || writeWait > maximumLSPBindTimeout {
		return nil, errors.New("bound LSP Gateway core and bounded write wait are required")
	}
	return &WebSocketLSPGateway{core: core, writeWait: writeWait}, nil
}

func (gateway *WebSocketLSPGateway) ServeBoundLSP(
	ctx context.Context,
	connection *websocket.Conn,
	grant lsp.TicketGrant,
	bind lsp.ClientBind,
) error {
	if gateway == nil || ctx == nil || connection == nil || bind.Profile.Validate() != nil ||
		bind.Profile.EffectiveLimits.MaxFrameBytes <= 0 {
		return errLSPGatewayFrame
	}
	connection.SetReadLimit(bind.Profile.EffectiveLimits.MaxFrameBytes)
	frames := newWebSocketLSPFrameConnection(
		connection, bind.Profile.EffectiveLimits.MaxFrameBytes, gateway.writeWait,
	)
	defer frames.Close()
	return gateway.core.Serve(ctx, frames, grant, bind)
}

type lspWebSocketFrameResult struct {
	value []byte
	err   error
}

// websocketLSPFrameConnection uses a one-frame reader pump. Logical Close
// closes only the pump channel, which immediately unblocks the Gateway while
// leaving the physical socket open long enough for LSPWebSocketHandler to send
// a stable close code. The handler's deferred physical Close then releases a
// pump currently blocked inside gorilla/websocket.
type websocketLSPFrameConnection struct {
	connection *websocket.Conn
	maximum    int64
	writeWait  time.Duration
	frames     chan lspWebSocketFrameResult
	closed     chan struct{}
	closeOnce  sync.Once
	writeMu    sync.Mutex
}

func newWebSocketLSPFrameConnection(
	connection *websocket.Conn,
	maximum int64,
	writeWait time.Duration,
) *websocketLSPFrameConnection {
	result := &websocketLSPFrameConnection{
		connection: connection, maximum: maximum, writeWait: writeWait,
		frames: make(chan lspWebSocketFrameResult, 1), closed: make(chan struct{}),
	}
	go result.readPump()
	return result
}

func (connection *websocketLSPFrameConnection) readPump() {
	for {
		messageType, value, err := connection.connection.ReadMessage()
		if err == nil && (messageType != websocket.TextMessage || len(value) == 0 ||
			int64(len(value)) > connection.maximum || !utf8.Valid(value)) {
			err = errLSPGatewayFrame
		}
		result := lspWebSocketFrameResult{value: append([]byte(nil), value...), err: err}
		select {
		case connection.frames <- result:
		case <-connection.closed:
			return
		}
		if err != nil {
			return
		}
	}
}

func (connection *websocketLSPFrameConnection) ReadFrame(ctx context.Context) ([]byte, error) {
	if connection == nil || ctx == nil {
		return nil, errLSPGatewayFrame
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-connection.closed:
		return nil, errLSPGatewayFrame
	case result := <-connection.frames:
		if result.err != nil {
			return nil, result.err
		}
		return append([]byte(nil), result.value...), nil
	}
}

func (connection *websocketLSPFrameConnection) WriteFrame(ctx context.Context, value []byte) error {
	if connection == nil || ctx == nil || len(value) == 0 || int64(len(value)) > connection.maximum || !utf8.Valid(value) {
		return errLSPGatewayFrame
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-connection.closed:
		return errLSPGatewayFrame
	default:
	}
	connection.writeMu.Lock()
	defer connection.writeMu.Unlock()
	select {
	case <-connection.closed:
		return errLSPGatewayFrame
	default:
	}
	deadline := time.Now().Add(connection.writeWait)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.connection.SetWriteDeadline(deadline); err != nil {
		return errLSPGatewayFrame
	}
	if err := connection.connection.WriteMessage(websocket.TextMessage, append([]byte(nil), value...)); err != nil {
		return errLSPGatewayFrame
	}
	return nil
}

func (connection *websocketLSPFrameConnection) Close() error {
	if connection == nil {
		return errLSPGatewayFrame
	}
	connection.closeOnce.Do(func() { close(connection.closed) })
	return nil
}

var _ LSPBindingGateway = (*WebSocketLSPGateway)(nil)
var _ lsp.FrameConnection = (*websocketLSPFrameConnection)(nil)
