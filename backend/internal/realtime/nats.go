package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/worksflow/builder/backend/internal/events"
)

type NATSFanout struct {
	jetStream nats.JetStreamContext
	hub       *Hub
	logger    *slog.Logger
}

func NewNATSFanout(jetStream nats.JetStreamContext, hub *Hub, logger *slog.Logger) *NATSFanout {
	return &NATSFanout{jetStream: jetStream, hub: hub, logger: logger}
}

func (f *NATSFanout) Run(ctx context.Context) error {
	errorsChannel, err := f.Start(ctx)
	if err != nil {
		return err
	}
	return <-errorsChannel
}

func (f *NATSFanout) Start(ctx context.Context) (<-chan error, error) {
	if f.jetStream == nil || f.hub == nil {
		return nil, errors.New("JetStream and realtime hub are required")
	}
	subscription, err := f.jetStream.SubscribeSync(
		events.DefaultSubject,
		nats.BindStream(events.DefaultStreamName),
		nats.DeliverNew(),
		nats.AckNone(),
	)
	if err != nil {
		return nil, err
	}
	result := make(chan error, 1)
	go func() {
		defer close(result)
		result <- f.consume(ctx, subscription)
	}()
	return result, nil
}

func (f *NATSFanout) consume(ctx context.Context, subscription *nats.Subscription) error {
	defer subscription.Unsubscribe()
	for {
		message, err := subscription.NextMsgWithContext(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ctx.Err()
			}
			return err
		}
		event, err := domainEventFromMessage(message)
		if err != nil {
			f.logger.Warn("discarding invalid realtime event", "error", err, "subject", message.Subject)
			continue
		}
		if !f.hub.Publish(event) {
			f.logger.Warn("realtime event queue is full", "event_id", event.ID)
		}
	}
}

func domainEventFromMessage(message *nats.Msg) (DomainEvent, error) {
	metadata, err := message.Metadata()
	if err != nil {
		return DomainEvent{}, err
	}
	return domainEventFromRaw(message.Subject, message.Header, message.Data, metadata.Sequence.Stream, metadata.Timestamp)
}

func domainEventFromRaw(subject string, header nats.Header, data []byte, sequence uint64, occurredAt time.Time) (DomainEvent, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		return DomainEvent{}, err
	}
	projectID, _ := payload["projectId"].(string)
	if projectID == "" {
		return DomainEvent{}, errors.New("event is missing projectId")
	}
	eventID := header.Get(nats.MsgIdHdr)
	if eventID == "" {
		eventID = uuid.NewString()
	}
	eventType := normalizeEventType(header.Get("Worksflow-Event-Type"))
	if eventType == "" {
		eventType = normalizeEventType(subject)
	}
	artifactID, _ := payload["artifactId"].(string)
	runID, _ := payload["runId"].(string)
	return DomainEvent{
		ID: eventID, Type: eventType, Cursor: sequence,
		ProjectID: projectID, ArtifactID: artifactID, RunID: runID,
		OccurredAt: occurredAt, Payload: append(json.RawMessage(nil), data...),
	}, nil
}

func normalizeEventType(value string) string {
	switch value {
	case "project.created", "project.updated", "worksflow.project.created", "worksflow.project.updated":
		return "project.updated"
	case "project.member_added", "project.member_role_updated", "project.member_removed",
		"worksflow.project.member.added", "worksflow.project.member.updated", "worksflow.project.member.removed":
		return "member.updated"
	default:
		return value
	}
}
