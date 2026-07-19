package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisConnectionTicketStoreConsumesOnceWithoutPlaintextKey(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDRESS"))
	if address == "" {
		t.Skip("WORKSFLOW_TEST_REDIS_ADDRESS is required for the real Redis ticket canary")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("connect Redis canary: %v", err)
	}
	now := sandboxBaseTime.Add(3 * time.Second)
	prefix := "worksflow:test:sandbox-ticket:" + uuid.NewString() + ":"
	store, err := NewRedisConnectionTicketStore(client, prefix, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("B", 43)
	grant := ConnectionTicketGrant{
		SchemaVersion: ConnectionTicketSchemaVersion,
		ID:            testCheckpoint, ProjectID: testProjectID, SessionID: testSessionID, ActorID: testActorID,
		SessionEpoch: 2, Origin: "https://builder.example",
		Channels: []StreamChannel{ChannelControl},
		Cursors:  []ConnectionCursor{{Channel: ChannelControl}},
		IssuedAt: now, ExpiresAt: now.Add(30 * time.Second),
	}
	if err := store.Put(ctx, secret, grant, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	keys, err := client.Keys(ctx, prefix+"*").Result()
	if err != nil || len(keys) != 1 || strings.Contains(keys[0], secret) {
		t.Fatalf("ticket key exposed plaintext or was not stored: keys=%#v err=%v", keys, err)
	}
	loaded, err := store.Consume(ctx, secret)
	if err != nil || loaded.ID != grant.ID || loaded.SessionEpoch != grant.SessionEpoch {
		t.Fatalf("consume exact ticket = %#v, %v", loaded, err)
	}
	if _, err := store.Consume(ctx, secret); err != ErrConnectionTicketConsumed {
		t.Fatalf("ticket replay error = %v", err)
	}
	if remaining, err := client.Keys(ctx, prefix+"*").Result(); err != nil || len(remaining) != 0 {
		t.Fatalf("consumed ticket was retained: keys=%#v err=%v", remaining, err)
	}
}
