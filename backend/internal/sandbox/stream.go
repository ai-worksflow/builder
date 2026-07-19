package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const SandboxStreamSchemaVersion = "sandbox-stream/v1"

var (
	ErrStreamInvalid           = errors.New("invalid sandbox stream event")
	ErrStreamUnavailable       = errors.New("sandbox stream store is unavailable")
	ErrStreamCursorUnavailable = errors.New("sandbox stream cursor is outside the retained window")
	ErrStreamReplayLimit       = errors.New("sandbox stream replay limit exceeded")
)

var streamEventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,79}$`)

type StreamEnvelope struct {
	SchemaVersion    string          `json:"schemaVersion"`
	SessionID        string          `json:"sessionId"`
	SessionEpoch     uint64          `json:"sessionEpoch"`
	Channel          StreamChannel   `json:"channel"`
	EventType        string          `json:"eventType"`
	Sequence         uint64          `json:"sequence"`
	AggregateVersion uint64          `json:"aggregateVersion"`
	RequestID        string          `json:"requestId,omitempty"`
	CorrelationID    string          `json:"correlationId,omitempty"`
	Timestamp        time.Time       `json:"timestamp"`
	Payload          json.RawMessage `json:"payload"`
}

type StreamEventInput struct {
	SessionID        string
	SessionEpoch     uint64
	Channel          StreamChannel
	EventType        string
	AggregateVersion uint64
	RequestID        string
	CorrelationID    string
	Payload          json.RawMessage
}

type StreamEventStore interface {
	Publish(context.Context, StreamEventInput) (StreamEnvelope, error)
	Replay(context.Context, string, uint64, StreamChannel, uint64, int) ([]StreamEnvelope, uint64, error)
	ReadAfter(context.Context, string, uint64, StreamChannel, uint64, int, time.Duration) ([]StreamEnvelope, error)
}

func normalizeStreamEventInput(input StreamEventInput) (StreamEventInput, error) {
	if !validUUID(input.SessionID) || input.SessionEpoch == 0 || !knownStreamChannel(input.Channel) {
		return StreamEventInput{}, ErrStreamInvalid
	}
	input.EventType = strings.TrimSpace(input.EventType)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	if !streamEventTypePattern.MatchString(input.EventType) || len(input.RequestID) > 200 || len(input.CorrelationID) > 200 ||
		strings.ContainsAny(input.RequestID+input.CorrelationID, "\r\n\x00") {
		return StreamEventInput{}, ErrStreamInvalid
	}
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage(`{}`)
	}
	if len(input.Payload) > 1<<20 || !json.Valid(input.Payload) {
		return StreamEventInput{}, ErrStreamInvalid
	}
	input.Payload = append(json.RawMessage(nil), input.Payload...)
	return input, nil
}

func validateStreamEnvelope(event StreamEnvelope) error {
	if event.SchemaVersion != SandboxStreamSchemaVersion || event.Sequence == 0 || event.Timestamp.IsZero() {
		return ErrStreamInvalid
	}
	normalized, err := normalizeStreamEventInput(StreamEventInput{
		SessionID: event.SessionID, SessionEpoch: event.SessionEpoch, Channel: event.Channel,
		EventType: event.EventType, AggregateVersion: event.AggregateVersion,
		RequestID: event.RequestID, CorrelationID: event.CorrelationID, Payload: event.Payload,
	})
	if err != nil || normalized.EventType != event.EventType || normalized.RequestID != event.RequestID ||
		normalized.CorrelationID != event.CorrelationID {
		return ErrStreamInvalid
	}
	return nil
}

func parseStreamSequenceID(value string) (uint64, error) {
	var sequence uint64
	var suffix int
	if _, err := fmt.Sscanf(value, "%d-%d", &sequence, &suffix); err != nil || sequence == 0 || suffix != 0 {
		return 0, ErrStreamInvalid
	}
	return sequence, nil
}
