package realtime

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type client struct {
	send chan Frame
}

type FrameType uint8

const (
	FrameText FrameType = iota + 1
	FrameBinary
)

type Frame struct {
	Type    FrameType
	Payload []byte
}

type DomainEvent struct {
	ID         string
	Type       string
	Cursor     uint64
	ProjectID  string
	ArtifactID string
	RunID      string
	OccurredAt time.Time
	Payload    json.RawMessage
}

type subscriptionCommand struct {
	client       *client
	subscription SubscriptionRequest
	remove       bool
	result       chan bool
}

type subscriptionState struct {
	request   SubscriptionRequest
	replaying bool
	pending   []DomainEvent
	overflow  bool
}

type beginSubscriptionResult struct {
	ok     bool
	cursor uint64
}

type beginSubscriptionCommand struct {
	client       *client
	subscription SubscriptionRequest
	result       chan beginSubscriptionResult
}

type completeSubscriptionCommand struct {
	client         *client
	subscriptionID string
	throughCursor  uint64
	initial        []Frame
	result         chan bool
}

type sendCommand struct {
	client *client
	frame  Frame
	result chan bool
}

type Hub struct {
	register      chan *client
	unregister    chan *client
	subscriptions chan subscriptionCommand
	begin         chan beginSubscriptionCommand
	complete      chan completeSubscriptionCommand
	direct        chan sendCommand
	events        chan DomainEvent
	done          chan struct{}
	clients       map[*client]map[string]*subscriptionState
	sendBuffer    int
	lastCursor    atomic.Uint64
	closeOnce     sync.Once
}

func NewHub(sendBuffer int) *Hub {
	if sendBuffer < 1 {
		sendBuffer = 1
	}
	return &Hub{
		register:      make(chan *client),
		unregister:    make(chan *client),
		subscriptions: make(chan subscriptionCommand),
		begin:         make(chan beginSubscriptionCommand),
		complete:      make(chan completeSubscriptionCommand),
		direct:        make(chan sendCommand),
		events:        make(chan DomainEvent, sendBuffer),
		done:          make(chan struct{}),
		clients:       make(map[*client]map[string]*subscriptionState),
		sendBuffer:    sendBuffer,
	}
}

func (h *Hub) Run(ctx context.Context) {
	defer h.close()
	for {
		select {
		case <-ctx.Done():
			return
		case connection := <-h.register:
			h.clients[connection] = make(map[string]*subscriptionState)
		case connection := <-h.unregister:
			h.remove(connection)
		case command := <-h.subscriptions:
			subscriptions, exists := h.clients[command.client]
			if exists {
				if command.remove {
					delete(subscriptions, command.subscription.ID)
				} else {
					subscriptions[command.subscription.ID] = &subscriptionState{request: command.subscription}
				}
			}
			command.result <- exists
		case command := <-h.begin:
			subscriptions, exists := h.clients[command.client]
			if exists {
				_, duplicate := subscriptions[command.subscription.ID]
				if duplicate {
					exists = false
				} else {
					subscriptions[command.subscription.ID] = &subscriptionState{
						request: command.subscription, replaying: true,
						pending: make([]DomainEvent, 0, h.sendBuffer),
					}
				}
			}
			command.result <- beginSubscriptionResult{ok: exists, cursor: h.lastCursor.Load()}
		case command := <-h.complete:
			command.result <- h.completeReplay(command)
		case command := <-h.direct:
			_, exists := h.clients[command.client]
			if exists {
				exists = h.enqueue(command.client, command.frame)
			}
			command.result <- exists
		case event := <-h.events:
			if event.Cursor > h.lastCursor.Load() {
				h.lastCursor.Store(event.Cursor)
			}
			h.publish(event)
		}
	}
}

func (h *Hub) registerClient(ctx context.Context) (*client, bool) {
	// Catch-up frames and events received while the history snapshot is being
	// read must coexist briefly. Live-event backpressure remains bounded by the
	// configured send buffer in publish.
	connection := &client{send: make(chan Frame, h.sendBuffer*2+1)}
	select {
	case h.register <- connection:
		return connection, true
	case <-ctx.Done():
		return nil, false
	case <-h.done:
		return nil, false
	}
}

// BeginReplaySubscription atomically installs a paused subscription and returns
// the last cursor already observed by the live fanout. Matching live events are
// buffered until CompleteReplay is called, closing the history/live race.
func (h *Hub) BeginReplaySubscription(ctx context.Context, connection *client, subscription SubscriptionRequest) (uint64, bool) {
	result := make(chan beginSubscriptionResult, 1)
	command := beginSubscriptionCommand{client: connection, subscription: subscription, result: result}
	select {
	case h.begin <- command:
	case <-ctx.Done():
		return 0, false
	case <-h.done:
		return 0, false
	}
	select {
	case value := <-result:
		return value.cursor, value.ok
	case <-ctx.Done():
		return 0, false
	case <-h.done:
		return 0, false
	}
}

// CompleteReplay enqueues the acknowledgement and replay frames first, then
// releases buffered live events newer than throughCursor in cursor order.
func (h *Hub) CompleteReplay(
	ctx context.Context,
	connection *client,
	subscriptionID string,
	throughCursor uint64,
	initial []Frame,
) bool {
	result := make(chan bool, 1)
	command := completeSubscriptionCommand{
		client: connection, subscriptionID: subscriptionID,
		throughCursor: throughCursor, initial: initial, result: result,
	}
	select {
	case h.complete <- command:
	case <-ctx.Done():
		return false
	case <-h.done:
		return false
	}
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		return false
	case <-h.done:
		return false
	}
}

func (h *Hub) unregisterClient(connection *client) {
	select {
	case h.unregister <- connection:
	case <-h.done:
	}
}

func (h *Hub) Subscribe(ctx context.Context, connection *client, subscription SubscriptionRequest) bool {
	result := make(chan bool, 1)
	command := subscriptionCommand{client: connection, subscription: subscription, result: result}
	select {
	case h.subscriptions <- command:
	case <-ctx.Done():
		return false
	case <-h.done:
		return false
	}
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		return false
	case <-h.done:
		return false
	}
}

func (h *Hub) Unsubscribe(ctx context.Context, connection *client, subscriptionID string) bool {
	result := make(chan bool, 1)
	command := subscriptionCommand{
		client: connection, subscription: SubscriptionRequest{ID: subscriptionID},
		remove: true, result: result,
	}
	select {
	case h.subscriptions <- command:
	case <-ctx.Done():
		return false
	case <-h.done:
		return false
	}
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		return false
	case <-h.done:
		return false
	}
}

func (h *Hub) Send(ctx context.Context, connection *client, frame Frame) bool {
	result := make(chan bool, 1)
	command := sendCommand{client: connection, frame: cloneFrame(frame), result: result}
	select {
	case h.direct <- command:
	case <-ctx.Done():
		return false
	case <-h.done:
		return false
	}
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		return false
	case <-h.done:
		return false
	}
}

func (h *Hub) Publish(event DomainEvent) bool {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	select {
	case h.events <- event:
		return true
	case <-h.done:
		return false
	default:
		return false
	}
}

func (h *Hub) Cursor() string {
	cursor := h.lastCursor.Load()
	if cursor == 0 {
		return ""
	}
	return strconv.FormatUint(cursor, 10)
}

func (h *Hub) publish(event DomainEvent) {
	for connection, subscriptions := range h.clients {
		for _, state := range subscriptions {
			if !matches(state.request, event) {
				continue
			}
			if state.replaying {
				if len(state.pending) >= h.sendBuffer {
					state.pending = nil
					state.overflow = true
				} else if !state.overflow {
					state.pending = append(state.pending, event)
				}
				continue
			}
			frame, err := eventFrame(state.request.ID, event)
			if err != nil || !h.enqueue(connection, frame) {
				break
			}
		}
	}
}

func (h *Hub) completeReplay(command completeSubscriptionCommand) bool {
	subscriptions, exists := h.clients[command.client]
	if !exists {
		return false
	}
	state, exists := subscriptions[command.subscriptionID]
	if !exists || !state.replaying || state.overflow {
		delete(subscriptions, command.subscriptionID)
		return false
	}
	for _, frame := range command.initial {
		if !h.enqueue(command.client, frame) {
			return false
		}
	}
	sort.SliceStable(state.pending, func(left, right int) bool {
		return state.pending[left].Cursor < state.pending[right].Cursor
	})
	for _, event := range state.pending {
		if event.Cursor <= command.throughCursor {
			continue
		}
		frame, err := eventFrame(state.request.ID, event)
		if err != nil || !h.enqueue(command.client, frame) {
			return false
		}
	}
	state.pending = nil
	state.replaying = false
	return true
}

func eventFrame(subscriptionID string, event DomainEvent) (Frame, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"type": "event",
		"event": map[string]interface{}{
			"id": event.ID, "type": event.Type,
			"cursor":         strconv.FormatUint(event.Cursor, 10),
			"subscriptionId": subscriptionID, "projectId": event.ProjectID,
			"occurredAt": event.OccurredAt.UTC().Format(time.RFC3339Nano),
			"payload":    json.RawMessage(event.Payload),
		},
	})
	return Frame{Type: FrameText, Payload: payload}, err
}

func matches(subscription SubscriptionRequest, event DomainEvent) bool {
	if subscription.ProjectID != event.ProjectID {
		return false
	}
	switch subscription.Topic {
	case "project":
		return true
	case "artifact":
		return subscription.ArtifactID != "" && subscription.ArtifactID == event.ArtifactID
	case "run":
		return subscription.RunID != "" && subscription.RunID == event.RunID
	default:
		return false
	}
}

func (h *Hub) enqueue(connection *client, frame Frame) bool {
	select {
	case connection.send <- cloneFrame(frame):
		return true
	default:
		h.remove(connection)
		return false
	}
}

func (h *Hub) remove(connection *client) {
	if _, exists := h.clients[connection]; !exists {
		return
	}
	delete(h.clients, connection)
	close(connection.send)
}

func (h *Hub) close() {
	h.closeOnce.Do(func() {
		close(h.done)
		for connection := range h.clients {
			delete(h.clients, connection)
			close(connection.send)
		}
	})
}

func cloneFrame(message Frame) Frame {
	return Frame{Type: message.Type, Payload: append([]byte(nil), message.Payload...)}
}
