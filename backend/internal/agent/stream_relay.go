package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/sandbox"
	"gorm.io/gorm"
)

var ErrAgentStreamDelivery = errors.New("AgentAttempt stream delivery is unavailable")

type StreamRelayConfig struct {
	BatchSize      int
	PollInterval   time.Duration
	ClaimTTL       time.Duration
	MaxAttempts    int
	PublishTimeout time.Duration
}

// StreamRelay delivers the immutable SQL AttemptEvent chain to the Session's
// retained WSS agent channel. Delivery is at least once across a crash between
// Redis publish and SQL acknowledgement; consumers deduplicate by the exact
// (attemptId, sequence) tuple carried in every payload.
type StreamRelay struct {
	database *gorm.DB
	events   sandbox.StreamEventStore
	config   StreamRelayConfig
	logger   *slog.Logger
}

func NewStreamRelay(
	database *gorm.DB,
	events sandbox.StreamEventStore,
	config StreamRelayConfig,
	logger *slog.Logger,
) (*StreamRelay, error) {
	if database == nil || events == nil || logger == nil {
		return nil, fmt.Errorf("%w: database, Sandbox stream, and logger are required", ErrAgentStreamDelivery)
	}
	if config.BatchSize < 1 || config.BatchSize > 500 ||
		config.PollInterval <= 0 || config.PollInterval > time.Minute ||
		config.ClaimTTL < time.Second || config.ClaimTTL > 10*time.Minute ||
		config.MaxAttempts < 1 || config.MaxAttempts > 100 ||
		config.PublishTimeout <= 0 || config.PublishTimeout >= config.ClaimTTL {
		return nil, fmt.Errorf("%w: relay configuration is invalid", ErrAgentStreamDelivery)
	}
	return &StreamRelay{database: database, events: events, config: config, logger: logger}, nil
}

type streamOutboxClaim struct {
	AttemptID       string `gorm:"column:attempt_id"`
	EventSequence   int64  `gorm:"column:event_sequence"`
	ClaimToken      string `gorm:"column:claim_token"`
	DeliveryAttempt int    `gorm:"column:delivery_attempts"`
}

type streamEventMetadata struct {
	SessionID    string `gorm:"column:session_id"`
	SessionEpoch int64  `gorm:"column:session_epoch"`
}

// DeliverBatch is exported for a deterministic worker tick and integration
// tests. It never acknowledges an outbox row before the retained stream has
// accepted its exact immutable AttemptEvent payload.
func (relay *StreamRelay) DeliverBatch(ctx context.Context) (int, error) {
	if relay == nil || ctx == nil {
		return 0, fmt.Errorf("%w: relay or context is unavailable", ErrAgentStreamDelivery)
	}
	claims, err := relay.claim(ctx)
	if err != nil {
		return 0, err
	}
	delivered := 0
	var deliveryErrors []error
	for _, claim := range claims {
		if err := relay.deliver(ctx, claim); err != nil {
			deliveryErrors = append(deliveryErrors, err)
			continue
		}
		delivered++
	}
	return delivered, errors.Join(deliveryErrors...)
}

func (relay *StreamRelay) Run(ctx context.Context) {
	if relay == nil || ctx == nil {
		return
	}
	ticker := time.NewTicker(relay.config.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := relay.DeliverBatch(ctx); err != nil && ctx.Err() == nil {
			relay.logger.Warn("deliver AgentAttempt stream outbox", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (relay *StreamRelay) Readiness(ctx context.Context) error {
	if relay == nil || ctx == nil {
		return fmt.Errorf("%w: relay or context is unavailable", ErrAgentStreamDelivery)
	}
	var exhausted int64
	result := relay.database.WithContext(ctx).Raw(`
SELECT count(*)
FROM agent_stream_outbox
WHERE delivered_at IS NULL AND delivery_attempts >= ?
`, relay.config.MaxAttempts).Scan(&exhausted)
	if result.Error != nil {
		return fmt.Errorf("%w: inspect stream outbox: %v", ErrAgentStreamDelivery, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: stream outbox readiness projection is unavailable", ErrAgentStreamDelivery)
	}
	if exhausted > 0 {
		return fmt.Errorf("%w: %d AgentAttempt stream events exhausted delivery", ErrAgentStreamDelivery, exhausted)
	}
	return nil
}

func (relay *StreamRelay) claim(ctx context.Context) ([]streamOutboxClaim, error) {
	token := uuid.NewString()
	claims := make([]streamOutboxClaim, 0, relay.config.BatchSize)
	result := relay.database.WithContext(ctx).Raw(`
WITH selected AS (
  SELECT attempt_id, event_sequence
  FROM agent_stream_outbox
  WHERE delivered_at IS NULL
    AND delivery_attempts < ?
    AND available_at <= clock_timestamp()
    AND (claim_token IS NULL OR claimed_until <= clock_timestamp())
  ORDER BY available_at, attempt_id, event_sequence
  FOR UPDATE SKIP LOCKED
  LIMIT ?
)
UPDATE agent_stream_outbox AS outbox
SET claim_token = ?::uuid,
    claimed_until = clock_timestamp() + (?::interval),
    delivery_attempts = outbox.delivery_attempts + 1,
    last_error = NULL,
    updated_at = clock_timestamp()
FROM selected
WHERE outbox.attempt_id = selected.attempt_id
  AND outbox.event_sequence = selected.event_sequence
RETURNING outbox.attempt_id::text AS attempt_id,
          outbox.event_sequence,
          outbox.claim_token::text AS claim_token,
          outbox.delivery_attempts
`, relay.config.MaxAttempts, relay.config.BatchSize, token, relay.config.ClaimTTL.String()).Scan(&claims)
	if result.Error != nil {
		return nil, fmt.Errorf("%w: claim stream outbox: %v", ErrAgentStreamDelivery, result.Error)
	}
	for _, claim := range claims {
		if !validUUIDs(claim.AttemptID, claim.ClaimToken) || claim.ClaimToken != token ||
			claim.EventSequence <= 0 || claim.DeliveryAttempt < 1 || claim.DeliveryAttempt > relay.config.MaxAttempts {
			return nil, fmt.Errorf("%w: invalid claimed outbox projection", ErrAgentStreamDelivery)
		}
	}
	return claims, nil
}

func (relay *StreamRelay) deliver(ctx context.Context, claim streamOutboxClaim) error {
	event, metadata, err := relay.loadEvent(ctx, claim)
	if err != nil {
		return relay.fail(ctx, claim, err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return relay.fail(ctx, claim, fmt.Errorf("encode immutable AttemptEvent: %w", err))
	}
	publishCtx, cancel := context.WithTimeout(ctx, relay.config.PublishTimeout)
	streamEvent, err := relay.events.Publish(publishCtx, sandbox.StreamEventInput{
		SessionID: metadata.SessionID, SessionEpoch: uint64(metadata.SessionEpoch),
		Channel: sandbox.ChannelAgent, EventType: "agent.attempt." + string(event.Kind),
		AggregateVersion: event.VersionTo, CorrelationID: event.AttemptID, Payload: payload,
	})
	cancel()
	if err != nil {
		return relay.fail(ctx, claim, fmt.Errorf("publish immutable AttemptEvent: %w", err))
	}
	if streamEvent.Channel != sandbox.ChannelAgent || streamEvent.SessionID != metadata.SessionID ||
		streamEvent.SessionEpoch != uint64(metadata.SessionEpoch) || streamEvent.Sequence == 0 {
		return relay.fail(ctx, claim, errors.New("Sandbox stream returned a different delivery identity"))
	}
	result := relay.database.WithContext(ctx).Exec(`
UPDATE agent_stream_outbox
SET delivered_at = clock_timestamp(),
    stream_sequence = ?,
    claim_token = NULL,
    claimed_until = NULL,
    last_error = NULL,
    updated_at = clock_timestamp()
WHERE attempt_id = ?
  AND event_sequence = ?
  AND claim_token = ?::uuid
  AND delivered_at IS NULL
`, int64(streamEvent.Sequence), claim.AttemptID, claim.EventSequence, claim.ClaimToken)
	if result.Error != nil {
		return fmt.Errorf("%w: acknowledge stream event: %v", ErrAgentStreamDelivery, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: stream event acknowledgement lost its claim", ErrAgentStreamDelivery)
	}
	return nil
}

func (relay *StreamRelay) loadEvent(
	ctx context.Context,
	claim streamOutboxClaim,
) (AttemptEvent, streamEventMetadata, error) {
	var row attemptEventRow
	result := relay.database.WithContext(ctx).Where(
		"attempt_id = ? AND sequence = ?", claim.AttemptID, claim.EventSequence,
	).Take(&row)
	if result.Error != nil {
		return AttemptEvent{}, streamEventMetadata{}, fmt.Errorf("load immutable AttemptEvent: %w", result.Error)
	}
	event, err := hydrateAttemptEvent(row)
	if err != nil {
		return AttemptEvent{}, streamEventMetadata{}, err
	}
	var metadata streamEventMetadata
	result = relay.database.WithContext(ctx).Raw(`
SELECT attempt.sandbox_session_id::text AS session_id,
       task.candidate_session_epoch AS session_epoch
FROM agent_attempts AS attempt
JOIN agent_task_capsules AS task
  ON task.id = attempt.task_capsule_id
 AND task.content_hash = attempt.task_capsule_hash
 AND task.project_id = attempt.project_id
JOIN sandbox_sessions AS session
  ON session.id = attempt.sandbox_session_id
 AND session.project_id = attempt.project_id
 AND session.candidate_id = attempt.candidate_id
WHERE attempt.id = ?
`, claim.AttemptID).Scan(&metadata)
	if result.Error != nil {
		return AttemptEvent{}, streamEventMetadata{}, fmt.Errorf(
			"load exact AgentAttempt Session stream identity: %w", result.Error,
		)
	}
	if result.RowsAffected != 1 || !validUUIDs(metadata.SessionID) || metadata.SessionEpoch <= 0 {
		return AttemptEvent{}, streamEventMetadata{}, errors.New(
			"exact AgentAttempt Session stream identity is unavailable",
		)
	}
	return event, metadata, nil
}

func (relay *StreamRelay) fail(
	ctx context.Context,
	claim streamOutboxClaim,
	deliveryErr error,
) error {
	message := strings.TrimSpace(deliveryErr.Error())
	if message == "" {
		message = ErrAgentStreamDelivery.Error()
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	backoff := time.Second
	for step := 1; step < claim.DeliveryAttempt && backoff < 5*time.Minute; step++ {
		backoff *= 2
	}
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	result := relay.database.WithContext(ctx).Exec(`
UPDATE agent_stream_outbox
SET claim_token = NULL,
    claimed_until = NULL,
    available_at = clock_timestamp() + (?::interval),
    last_error = ?,
    updated_at = clock_timestamp()
WHERE attempt_id = ?
  AND event_sequence = ?
  AND claim_token = ?::uuid
  AND delivered_at IS NULL
`, backoff.String(), message, claim.AttemptID, claim.EventSequence, claim.ClaimToken)
	if result.Error != nil {
		return errors.Join(
			fmt.Errorf("%w: release failed stream claim: %v", ErrAgentStreamDelivery, result.Error),
			deliveryErr,
		)
	}
	if result.RowsAffected != 1 {
		return errors.Join(
			fmt.Errorf("%w: failed stream claim lost its fence", ErrAgentStreamDelivery),
			deliveryErr,
		)
	}
	return fmt.Errorf("%w: %v", ErrAgentStreamDelivery, deliveryErr)
}
