package realtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go"
)

var (
	ErrCursorUnavailable = errors.New("realtime cursor is no longer available")
	ErrReplayLimit       = errors.New("realtime replay limit exceeded")
)

// HistoryReader exposes a stable JetStream snapshot to a WebSocket subscription.
// Cursors are global stream sequence numbers, so Replay scans the bounded sequence
// window and applies the same resource filter as the live hub.
type HistoryReader interface {
	Bounds(context.Context) (first uint64, last uint64, err error)
	Replay(context.Context, SubscriptionRequest, uint64, uint64, int) ([]DomainEvent, error)
}

type NATSHistoryReader struct {
	jetStream nats.JetStreamContext
	stream    string
}

func NewNATSHistoryReader(jetStream nats.JetStreamContext, stream string) (*NATSHistoryReader, error) {
	if jetStream == nil {
		return nil, errors.New("JetStream is required")
	}
	if stream == "" {
		return nil, errors.New("stream name is required")
	}
	return &NATSHistoryReader{jetStream: jetStream, stream: stream}, nil
}

func (r *NATSHistoryReader) Bounds(ctx context.Context) (uint64, uint64, error) {
	info, err := r.jetStream.StreamInfo(r.stream, nats.Context(ctx))
	if err != nil {
		return 0, 0, fmt.Errorf("inspect realtime stream: %w", err)
	}
	if info.State.Msgs == 0 {
		return 0, 0, nil
	}
	return info.State.FirstSeq, info.State.LastSeq, nil
}

func (r *NATSHistoryReader) Replay(
	ctx context.Context,
	subscription SubscriptionRequest,
	after uint64,
	through uint64,
	limit int,
) ([]DomainEvent, error) {
	if limit < 1 {
		return nil, errors.New("replay limit must be positive")
	}
	if through <= after {
		return []DomainEvent{}, nil
	}
	// The stream cursor is global. Bound the scanned sequence window as well as
	// the returned events so one reconnect cannot monopolize the API process.
	if through-after > uint64(limit) {
		return nil, ErrReplayLimit
	}

	replayed := make([]DomainEvent, 0, min(limit, int(through-after)))
	for sequence := after + 1; sequence <= through; sequence++ {
		raw, err := r.jetStream.GetMsg(r.stream, sequence, nats.Context(ctx))
		if errors.Is(err, nats.ErrMsgNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read realtime event %d: %w", sequence, err)
		}
		event, err := domainEventFromRaw(raw.Subject, raw.Header, raw.Data, raw.Sequence, raw.Time)
		if err != nil {
			return nil, fmt.Errorf("decode realtime event %d: %w", sequence, err)
		}
		if !matches(subscription, event) {
			continue
		}
		if len(replayed) >= limit {
			return nil, ErrReplayLimit
		}
		replayed = append(replayed, event)
	}
	return replayed, nil
}
