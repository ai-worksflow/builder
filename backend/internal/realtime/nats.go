package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
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

type FanoutStarter interface {
	Start(context.Context) (<-chan error, error)
}

type FanoutSupervisorConfig struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	StableAfter    time.Duration
}

type FanoutSupervisor struct {
	worker         FanoutStarter
	logger         *slog.Logger
	initialBackoff time.Duration
	maxBackoff     time.Duration
	stableAfter    time.Duration

	mu      sync.RWMutex
	healthy bool
}

func NewFanoutSupervisor(worker FanoutStarter, logger *slog.Logger, config FanoutSupervisorConfig) (*FanoutSupervisor, error) {
	if worker == nil || logger == nil {
		return nil, errors.New("realtime fanout worker and logger are required")
	}
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = 250 * time.Millisecond
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 30 * time.Second
	}
	if config.StableAfter <= 0 {
		config.StableAfter = 30 * time.Second
	}
	if config.MaxBackoff < config.InitialBackoff || config.StableAfter < config.InitialBackoff {
		return nil, errors.New("realtime fanout supervisor backoff configuration is invalid")
	}
	return &FanoutSupervisor{
		worker: worker, logger: logger, initialBackoff: config.InitialBackoff,
		maxBackoff: config.MaxBackoff, stableAfter: config.StableAfter,
	}, nil
}

func (s *FanoutSupervisor) Run(ctx context.Context) {
	backoff := s.initialBackoff
	defer s.setHealthy(false)
	for ctx.Err() == nil {
		errorsChannel, err := s.worker.Start(ctx)
		if err == nil && errorsChannel == nil {
			err = errors.New("realtime fanout worker returned no lifecycle channel")
		}
		if err != nil {
			s.setHealthy(false)
			s.logger.Error("NATS realtime fanout subscription failed", "error", err, "retry_in", backoff)
			if !waitFanoutRetry(ctx, backoff) {
				return
			}
			backoff = nextFanoutBackoff(backoff, s.maxBackoff)
			continue
		}
		s.setHealthy(true)
		stableTimer := time.NewTimer(s.stableAfter)
		stable := false
		workerErr, open := error(nil), true
		select {
		case <-ctx.Done():
			stopTimer(stableTimer)
			return
		case workerErr, open = <-errorsChannel:
			stopTimer(stableTimer)
		case <-stableTimer.C:
			stable = true
			select {
			case <-ctx.Done():
				return
			case workerErr, open = <-errorsChannel:
			}
		}
		if ctx.Err() != nil {
			return
		}
		if !open || workerErr == nil {
			workerErr = errors.New("realtime fanout worker stopped unexpectedly")
		}
		s.setHealthy(false)
		if stable {
			backoff = s.initialBackoff
		}
		s.logger.Error("NATS realtime fanout stopped", "error", workerErr, "retry_in", backoff)
		if !waitFanoutRetry(ctx, backoff) {
			return
		}
		backoff = nextFanoutBackoff(backoff, s.maxBackoff)
	}
}

func (s *FanoutSupervisor) Readiness(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.RLock()
	healthy := s.healthy
	s.mu.RUnlock()
	if !healthy {
		return errors.New("realtime fanout subscription is not active")
	}
	return nil
}

func (s *FanoutSupervisor) setHealthy(healthy bool) {
	s.mu.Lock()
	s.healthy = healthy
	s.mu.Unlock()
}

func waitFanoutRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextFanoutBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}
	return current * 2
}

func stopTimer(timer *time.Timer) {
	if timer != nil && !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
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
	options := []nats.SubOpt{
		nats.BindStream(events.DefaultStreamName),
		nats.AckNone(),
	}
	if cursor := f.hub.lastCursor.Load(); cursor > 0 && cursor < ^uint64(0) {
		options = append(options, nats.StartSequence(cursor+1))
	} else {
		options = append(options, nats.DeliverNew())
	}
	subscription, err := f.jetStream.SubscribeSync(
		events.DefaultSubject,
		options...,
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
	// Workflow node event names are intentionally extensible. The wire contract
	// exposes their stable envelope type and keeps the concrete node event in
	// the payload instead of requiring every frontend to know every node type.
	if subject == "worksflow.workflow.run.event" {
		eventType = "run.event"
	}
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
