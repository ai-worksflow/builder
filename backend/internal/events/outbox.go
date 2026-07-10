package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	DefaultStreamName = "WORKSFLOW_EVENTS"
	DefaultSubject    = "worksflow.>"
)

type OutboxConfig struct {
	BatchSize    int
	PollInterval time.Duration
	ClaimTTL     time.Duration
	MaxAttempts  int
	PublishWait  time.Duration
}

func DefaultOutboxConfig() OutboxConfig {
	return OutboxConfig{
		BatchSize: 50, PollInterval: 500 * time.Millisecond,
		ClaimTTL: 30 * time.Second, MaxAttempts: 20, PublishWait: 5 * time.Second,
	}
}

type OutboxPublisher struct {
	database  *gorm.DB
	jetStream nats.JetStreamContext
	logger    *slog.Logger
	config    OutboxConfig
	now       func() time.Time
}

func NewOutboxPublisher(database *gorm.DB, jetStream nats.JetStreamContext, logger *slog.Logger, config OutboxConfig) (*OutboxPublisher, error) {
	if database == nil || jetStream == nil || logger == nil {
		return nil, errors.New("database, JetStream and logger are required")
	}
	if config.BatchSize <= 0 || config.PollInterval <= 0 || config.ClaimTTL <= 0 ||
		config.MaxAttempts <= 0 || config.PublishWait <= 0 {
		return nil, errors.New("outbox configuration values must be positive")
	}
	return &OutboxPublisher{
		database: database, jetStream: jetStream, logger: logger, config: config, now: time.Now,
	}, nil
}

func EnsureEventStream(ctx context.Context, jetStream nats.JetStreamContext) error {
	if jetStream == nil {
		return errors.New("JetStream is required")
	}
	if _, err := jetStream.StreamInfo(DefaultStreamName, nats.Context(ctx)); err == nil {
		return nil
	} else if !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("inspect event stream: %w", err)
	}
	_, err := jetStream.AddStream(&nats.StreamConfig{
		Name: DefaultStreamName, Subjects: []string{DefaultSubject}, Storage: nats.FileStorage,
		Retention: nats.LimitsPolicy, Discard: nats.DiscardOld, MaxAge: 30 * 24 * time.Hour,
		Duplicates: 10 * time.Minute,
	}, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("create event stream: %w", err)
	}
	return nil
}

func (p *OutboxPublisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.config.PollInterval)
	defer ticker.Stop()
	for {
		if err := p.PublishBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.Error("outbox batch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *OutboxPublisher) PublishBatch(ctx context.Context) error {
	events, err := p.claim(ctx)
	if err != nil {
		return err
	}
	var batchErrors []error
	for _, event := range events {
		if err := p.publish(ctx, event); err != nil {
			batchErrors = append(batchErrors, fmt.Errorf("publish outbox event %s: %w", event.ID, err))
		}
	}
	return errors.Join(batchErrors...)
}

func (p *OutboxPublisher) claim(ctx context.Context) ([]storage.OutboxEventModel, error) {
	now := p.now().UTC()
	claimedUntil := now.Add(p.config.ClaimTTL)
	var events []storage.OutboxEventModel
	err := p.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("published_at IS NULL AND available_at <= ? AND attempts < ?", now, p.config.MaxAttempts).
			Order("created_at ASC").Limit(p.config.BatchSize).
			Find(&events).Error; err != nil {
			return err
		}
		for index := range events {
			result := transaction.Model(&storage.OutboxEventModel{}).
				Where("id = ? AND published_at IS NULL", events[index].ID).
				Updates(map[string]any{
					"attempts": gorm.Expr("attempts + 1"), "available_at": claimedUntil,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("outbox event %s was claimed concurrently", events[index].ID)
			}
			events[index].Attempts++
			events[index].AvailableAt = claimedUntil
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim outbox batch: %w", err)
	}
	return events, nil
}

func (p *OutboxPublisher) publish(ctx context.Context, event storage.OutboxEventModel) error {
	message := nats.NewMsg(event.Subject)
	message.Data = event.Payload
	message.Header.Set(nats.MsgIdHdr, event.ID.String())
	message.Header.Set("Worksflow-Event-Type", event.EventType)
	message.Header.Set("Worksflow-Aggregate-Type", event.AggregateType)
	message.Header.Set("Worksflow-Aggregate-ID", event.AggregateID)
	var customHeaders map[string]string
	if len(event.Headers) > 0 && json.Unmarshal(event.Headers, &customHeaders) == nil {
		for key, value := range customHeaders {
			message.Header.Set(key, value)
		}
	}
	publishCtx, cancel := context.WithTimeout(ctx, p.config.PublishWait)
	defer cancel()
	if _, err := p.jetStream.PublishMsg(message, nats.Context(publishCtx)); err != nil {
		p.recordFailure(ctx, event, err)
		return err
	}
	now := p.now().UTC()
	result := p.database.WithContext(ctx).Model(&storage.OutboxEventModel{}).
		Where("id = ? AND published_at IS NULL", event.ID).
		Updates(map[string]any{"published_at": now, "last_error": nil})
	if result.Error != nil {
		return fmt.Errorf("mark event published: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("event disappeared before it could be marked published")
	}
	return nil
}

func (p *OutboxPublisher) recordFailure(ctx context.Context, event storage.OutboxEventModel, cause error) {
	retryDelay := time.Second * time.Duration(1<<min(event.Attempts, 8))
	nextAttempt := p.now().UTC().Add(retryDelay)
	message := cause.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	_ = p.database.WithContext(ctx).Model(&storage.OutboxEventModel{}).
		Where("id = ? AND published_at IS NULL", event.ID).
		Updates(map[string]any{"available_at": nextAttempt, "last_error": message}).Error
}
