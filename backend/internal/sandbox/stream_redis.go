package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var publishSandboxStreamScript = redis.NewScript(`
local sequence = redis.call('INCR', KEYS[1])
redis.call(
  'XADD', KEYS[2], 'MAXLEN', '=', ARGV[1], tostring(sequence) .. '-0',
  'schemaVersion', ARGV[2],
  'sessionId', ARGV[3],
  'sessionEpoch', ARGV[4],
  'channel', ARGV[5],
  'eventType', ARGV[6],
  'aggregateVersion', ARGV[7],
  'requestId', ARGV[8],
  'correlationId', ARGV[9],
  'timestamp', ARGV[10],
  'payload', ARGV[11]
)
redis.call('EXPIRE', KEYS[1], ARGV[12])
redis.call('EXPIRE', KEYS[2], ARGV[12])
return sequence
`)

type RedisStreamEventStore struct {
	client    redis.UniversalClient
	prefix    string
	maxEvents int
	retention time.Duration
	now       func() time.Time
}

func NewRedisStreamEventStore(
	client redis.UniversalClient,
	prefix string,
	maxEvents int,
	retention time.Duration,
	now func() time.Time,
) (*RedisStreamEventStore, error) {
	if client == nil || now == nil {
		return nil, fmt.Errorf("%w: Redis client and clock are required", ErrStreamUnavailable)
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "worksflow:sandbox:stream:"
	}
	if len(prefix) > 200 || strings.ContainsAny(prefix, "\r\n\x00") || maxEvents < 16 || maxEvents > 100_000 ||
		retention < time.Minute || retention > 7*24*time.Hour {
		return nil, ErrStreamInvalid
	}
	return &RedisStreamEventStore{
		client: client, prefix: prefix, maxEvents: maxEvents, retention: retention, now: now,
	}, nil
}

func (store *RedisStreamEventStore) Publish(
	ctx context.Context,
	input StreamEventInput,
) (StreamEnvelope, error) {
	if store == nil || ctx == nil {
		return StreamEnvelope{}, ErrStreamInvalid
	}
	normalized, err := normalizeStreamEventInput(input)
	if err != nil {
		return StreamEnvelope{}, err
	}
	now := store.now().UTC()
	if now.IsZero() {
		return StreamEnvelope{}, ErrStreamInvalid
	}
	sequenceValue, err := publishSandboxStreamScript.Run(
		ctx, store.client,
		[]string{store.sequenceKey(normalized), store.streamKey(normalized.SessionID, normalized.SessionEpoch, normalized.Channel)},
		store.maxEvents, SandboxStreamSchemaVersion, normalized.SessionID, normalized.SessionEpoch,
		string(normalized.Channel), normalized.EventType, normalized.AggregateVersion,
		normalized.RequestID, normalized.CorrelationID, now.Format(time.RFC3339Nano), string(normalized.Payload),
		int64(store.retention/time.Second),
	).Uint64()
	if err != nil || sequenceValue == 0 {
		return StreamEnvelope{}, fmt.Errorf("%w: publish event: %v", ErrStreamUnavailable, err)
	}
	event := StreamEnvelope{
		SchemaVersion: SandboxStreamSchemaVersion,
		SessionID:     normalized.SessionID, SessionEpoch: normalized.SessionEpoch,
		Channel: normalized.Channel, EventType: normalized.EventType, Sequence: sequenceValue,
		AggregateVersion: normalized.AggregateVersion,
		RequestID:        normalized.RequestID, CorrelationID: normalized.CorrelationID,
		Timestamp: now, Payload: append(json.RawMessage(nil), normalized.Payload...),
	}
	return event, nil
}

func (store *RedisStreamEventStore) Replay(
	ctx context.Context,
	sessionID string,
	sessionEpoch uint64,
	channel StreamChannel,
	after uint64,
	limit int,
) ([]StreamEnvelope, uint64, error) {
	if err := validateStreamRead(ctx, sessionID, sessionEpoch, channel, limit); err != nil {
		return nil, 0, err
	}
	key := store.streamKey(sessionID, sessionEpoch, channel)
	first, last, err := store.bounds(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	if last == 0 {
		if after != 0 {
			return nil, 0, ErrStreamCursorUnavailable
		}
		return []StreamEnvelope{}, 0, nil
	}
	if after > last || (first > 1 && after < first-1) {
		return nil, last, ErrStreamCursorUnavailable
	}
	messages, err := store.client.XRangeN(ctx, key, "("+strconv.FormatUint(after, 10)+"-0", "+", int64(limit+1)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, last, fmt.Errorf("%w: replay events: %v", ErrStreamUnavailable, err)
	}
	if len(messages) > limit {
		return nil, last, ErrStreamReplayLimit
	}
	events, err := decodeStreamMessages(messages, sessionID, sessionEpoch, channel)
	if err != nil {
		return nil, last, err
	}
	return events, last, nil
}

func (store *RedisStreamEventStore) ReadAfter(
	ctx context.Context,
	sessionID string,
	sessionEpoch uint64,
	channel StreamChannel,
	after uint64,
	limit int,
	block time.Duration,
) ([]StreamEnvelope, error) {
	if err := validateStreamRead(ctx, sessionID, sessionEpoch, channel, limit); err != nil || block <= 0 || block > time.Minute {
		return nil, ErrStreamInvalid
	}
	streams, err := store.client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{store.streamKey(sessionID, sessionEpoch, channel), strconv.FormatUint(after, 10) + "-0"},
		Count:   int64(limit), Block: block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return []StreamEnvelope{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read events: %v", ErrStreamUnavailable, err)
	}
	messages := make([]redis.XMessage, 0, limit)
	for _, stream := range streams {
		messages = append(messages, stream.Messages...)
	}
	return decodeStreamMessages(messages, sessionID, sessionEpoch, channel)
}

func (store *RedisStreamEventStore) bounds(ctx context.Context, key string) (uint64, uint64, error) {
	firstMessages, err := store.client.XRangeN(ctx, key, "-", "+", 1).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, fmt.Errorf("%w: read first cursor: %v", ErrStreamUnavailable, err)
	}
	if len(firstMessages) == 0 {
		return 0, 0, nil
	}
	lastMessages, err := store.client.XRevRangeN(ctx, key, "+", "-", 1).Result()
	if err != nil || len(lastMessages) != 1 {
		return 0, 0, fmt.Errorf("%w: read last cursor: %v", ErrStreamUnavailable, err)
	}
	first, err := parseStreamSequenceID(firstMessages[0].ID)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: first cursor is corrupt", ErrStreamUnavailable)
	}
	last, err := parseStreamSequenceID(lastMessages[0].ID)
	if err != nil || last < first {
		return 0, 0, fmt.Errorf("%w: last cursor is corrupt", ErrStreamUnavailable)
	}
	return first, last, nil
}

func decodeStreamMessages(
	messages []redis.XMessage,
	sessionID string,
	sessionEpoch uint64,
	channel StreamChannel,
) ([]StreamEnvelope, error) {
	result := make([]StreamEnvelope, 0, len(messages))
	var previous uint64
	for _, message := range messages {
		sequence, err := parseStreamSequenceID(message.ID)
		if err != nil || sequence <= previous {
			return nil, fmt.Errorf("%w: stream sequence is corrupt", ErrStreamUnavailable)
		}
		previous = sequence
		event, err := decodeStreamMessage(message.Values, sequence)
		if err != nil || event.SessionID != sessionID || event.SessionEpoch != sessionEpoch || event.Channel != channel {
			return nil, fmt.Errorf("%w: stream event is corrupt", ErrStreamUnavailable)
		}
		result = append(result, event)
	}
	return result, nil
}

func decodeStreamMessage(values map[string]interface{}, sequence uint64) (StreamEnvelope, error) {
	value := func(name string) string {
		if raw, ok := values[name]; ok {
			return fmt.Sprint(raw)
		}
		return ""
	}
	epoch, err := strconv.ParseUint(value("sessionEpoch"), 10, 64)
	if err != nil {
		return StreamEnvelope{}, err
	}
	aggregate, err := strconv.ParseUint(value("aggregateVersion"), 10, 64)
	if err != nil {
		return StreamEnvelope{}, err
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value("timestamp"))
	if err != nil {
		return StreamEnvelope{}, err
	}
	event := StreamEnvelope{
		SchemaVersion: value("schemaVersion"), SessionID: value("sessionId"), SessionEpoch: epoch,
		Channel: StreamChannel(value("channel")), EventType: value("eventType"), Sequence: sequence,
		AggregateVersion: aggregate, RequestID: value("requestId"), CorrelationID: value("correlationId"),
		Timestamp: timestamp.UTC(), Payload: json.RawMessage(value("payload")),
	}
	return event, validateStreamEnvelope(event)
}

func validateStreamRead(ctx context.Context, sessionID string, sessionEpoch uint64, channel StreamChannel, limit int) error {
	if ctx == nil || !validUUID(sessionID) || sessionEpoch == 0 || !knownStreamChannel(channel) || limit < 1 || limit > 10_000 {
		return ErrStreamInvalid
	}
	return nil
}

func (store *RedisStreamEventStore) sequenceKey(input StreamEventInput) string {
	return store.prefix + input.SessionID + ":" + strconv.FormatUint(input.SessionEpoch, 10) + ":" + string(input.Channel) + ":sequence"
}

func (store *RedisStreamEventStore) streamKey(sessionID string, sessionEpoch uint64, channel StreamChannel) string {
	return store.prefix + sessionID + ":" + strconv.FormatUint(sessionEpoch, 10) + ":" + string(channel) + ":events"
}

var _ StreamEventStore = (*RedisStreamEventStore)(nil)
