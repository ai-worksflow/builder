package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/worksflow/builder/backend/internal/lsp"
)

type lspGatewayCoreFake struct {
	serve func(context.Context, lsp.FrameConnection, lsp.TicketGrant, lsp.ClientBind) error
}

func (fake lspGatewayCoreFake) Serve(
	ctx context.Context,
	connection lsp.FrameConnection,
	grant lsp.TicketGrant,
	bind lsp.ClientBind,
) error {
	return fake.serve(ctx, connection, grant, bind)
}

func TestWebSocketLSPGatewayAdaptsBoundedTextFramesWithoutOwningPhysicalClose(t *testing.T) {
	var received string
	var receiveMu sync.Mutex
	finished := make(chan error, 1)
	core := lspGatewayCoreFake{serve: func(
		ctx context.Context,
		connection lsp.FrameConnection,
		_ lsp.TicketGrant,
		_ lsp.ClientBind,
	) error {
		if err := connection.WriteFrame(ctx, []byte(`{"kind":"server.test"}`)); err != nil {
			return err
		}
		value, err := connection.ReadFrame(ctx)
		if err != nil {
			return err
		}
		receiveMu.Lock()
		received = string(value)
		receiveMu.Unlock()
		return errors.New("typed core result")
	}}
	gateway, err := NewWebSocketLSPGateway(core, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	bind := lsp.ClientBind{Profile: lspWebSocketProfile(t)}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, upgradeErr := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).
			Upgrade(writer, request, nil)
		if upgradeErr != nil {
			finished <- upgradeErr
			return
		}
		defer connection.Close()
		finished <- gateway.ServeBoundLSP(request.Context(), connection, lsp.TicketGrant{}, bind)
	}))
	defer server.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	messageType, value, err := client.ReadMessage()
	if err != nil || messageType != websocket.TextMessage || string(value) != `{"kind":"server.test"}` {
		t.Fatalf("server frame = type %d value %q err %v", messageType, value, err)
	}
	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"kind":"client.test"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if result == nil || result.Error() != "typed core result" {
			t.Fatalf("adapter erased typed core result: %v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bound Gateway did not finish")
	}
	receiveMu.Lock()
	defer receiveMu.Unlock()
	if received != `{"kind":"client.test"}` {
		t.Fatalf("client frame = %q", received)
	}
}

func TestWebSocketLSPFrameConnectionRejectsBinaryAndLogicalCloseUnblocksRead(t *testing.T) {
	for _, test := range []struct {
		name        string
		messageType int
		closeFirst  bool
	}{
		{name: "binary", messageType: websocket.BinaryMessage},
		{name: "logical close", closeFirst: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			finished := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				connection, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).
					Upgrade(writer, request, nil)
				if err != nil {
					finished <- err
					return
				}
				defer connection.Close()
				frames := newWebSocketLSPFrameConnection(connection, 1024, time.Second)
				if test.closeFirst {
					_ = frames.Close()
				}
				_, err = frames.ReadFrame(context.Background())
				finished <- err
			}))
			defer server.Close()
			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if !test.closeFirst {
				if err := client.WriteMessage(test.messageType, []byte("not text")); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-finished:
				if !errors.Is(err, errLSPGatewayFrame) {
					t.Fatalf("frame error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("frame read did not unblock")
			}
		})
	}
}
