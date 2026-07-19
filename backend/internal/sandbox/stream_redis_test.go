package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisSandboxStreamMonotonicReplayWindowAndBlockingRead(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDRESS"))
	if address == "" {
		t.Skip("WORKSFLOW_TEST_REDIS_ADDRESS is required for the real Redis stream canary")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	now := sandboxBaseTime
	prefix := "worksflow:test:sandbox-stream:" + uuid.NewString() + ":"
	store, err := NewRedisStreamEventStore(client, prefix, 16, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	const events = 24
	payloads := make([]json.RawMessage, events)
	for index := range payloads {
		payloads[index] = mustStreamJSON(t, map[string]any{"index": index})
	}
	sequences := make(chan uint64, events)
	errorsSeen := make(chan error, events)
	var workers sync.WaitGroup
	for index := 0; index < events; index++ {
		index := index
		workers.Add(1)
		go func() {
			defer workers.Done()
			event, publishErr := store.Publish(ctx, StreamEventInput{
				SessionID: testSessionID, SessionEpoch: 3, Channel: ChannelProcess,
				EventType: "process.output", AggregateVersion: 7,
				RequestID: "request", Payload: payloads[index],
			})
			if publishErr != nil {
				errorsSeen <- publishErr
				return
			}
			sequences <- event.Sequence
		}()
	}
	workers.Wait()
	close(errorsSeen)
	close(sequences)
	for err := range errorsSeen {
		t.Fatalf("concurrent publish: %v", err)
	}
	seen := make(map[uint64]bool, events)
	for sequence := range sequences {
		if seen[sequence] || sequence == 0 {
			t.Fatalf("duplicate sequence %d", sequence)
		}
		seen[sequence] = true
	}
	if len(seen) != events {
		t.Fatalf("sequence count=%d", len(seen))
	}

	if _, through, err := store.Replay(ctx, testSessionID, 3, ChannelProcess, 0, 16); !errors.Is(err, ErrStreamCursorUnavailable) || through != events {
		t.Fatalf("trimmed cursor did not reset: through=%d err=%v", through, err)
	}
	replayed, through, err := store.Replay(ctx, testSessionID, 3, ChannelProcess, 8, 16)
	if err != nil || len(replayed) != 16 || through != events || replayed[0].Sequence != 9 || replayed[15].Sequence != events {
		t.Fatalf("exact replay mismatch: len=%d through=%d first=%#v err=%v", len(replayed), through, firstStreamEvent(replayed), err)
	}

	readResult := make(chan []StreamEnvelope, 1)
	readError := make(chan error, 1)
	go func() {
		values, readErr := store.ReadAfter(ctx, testSessionID, 3, ChannelProcess, events, 4, 3*time.Second)
		readResult <- values
		readError <- readErr
	}()
	time.Sleep(25 * time.Millisecond)
	published, err := store.Publish(ctx, StreamEventInput{
		SessionID: testSessionID, SessionEpoch: 3, Channel: ChannelProcess,
		EventType: "process.exited", AggregateVersion: 8, Payload: json.RawMessage(`{"exitCode":0}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	values := <-readResult
	if err := <-readError; err != nil || len(values) != 1 || values[0].Sequence != published.Sequence || values[0].EventType != "process.exited" {
		t.Fatalf("blocking read = %#v, %v", values, err)
	}

	keys, err := client.Keys(ctx, prefix+"*").Result()
	if err != nil || len(keys) != 2 {
		t.Fatalf("unexpected Redis stream keys: %#v err=%v", keys, err)
	}
	for _, key := range keys {
		_ = client.Del(ctx, key).Err()
	}
}

func TestSandboxStreamRejectsInvalidPayloadAndCrossChannelDecode(t *testing.T) {
	if _, err := normalizeStreamEventInput(StreamEventInput{
		SessionID: testSessionID, SessionEpoch: 1, Channel: ChannelPTY,
		EventType: "PTY OUTPUT", Payload: json.RawMessage(`not-json`),
	}); !errors.Is(err, ErrStreamInvalid) {
		t.Fatalf("invalid stream input error = %v", err)
	}
}

func mustStreamJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func firstStreamEvent(values []StreamEnvelope) StreamEnvelope {
	if len(values) == 0 {
		return StreamEnvelope{}
	}
	return values[0]
}
