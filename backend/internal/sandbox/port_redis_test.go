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

func TestRedisPreviewGrantStoreIsReusableWithoutPlaintextKey(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDRESS"))
	if address == "" {
		t.Skip("WORKSFLOW_TEST_REDIS_ADDRESS is required for the real Redis preview canary")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	now := sandboxBaseTime.Add(5 * time.Second)
	prefix := "worksflow:test:sandbox-preview:" + uuid.NewString() + ":"
	store, err := NewRedisPreviewGrantStore(client, prefix, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("b", 48)
	grant := PreviewGrant{
		SchemaVersion: PreviewGrantSchemaVersion, ID: testCheckpoint,
		ProjectID: testProjectID, SessionID: testSessionID, SessionEpoch: 2, ActorID: testActorID,
		PortName: "web-http", PortNumber: 3000, Protocol: "http",
		IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	if err := store.Put(ctx, secret, grant, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	defer func() {
		keys, _ := client.Keys(ctx, prefix+"*").Result()
		if len(keys) > 0 {
			_ = client.Del(ctx, keys...).Err()
		}
	}()
	keys, err := client.Keys(ctx, prefix+"*").Result()
	if err != nil || len(keys) != 1 || strings.Contains(keys[0], secret) {
		t.Fatalf("preview key exposed plaintext or was not stored: keys=%#v err=%v", keys, err)
	}
	for index := 0; index < 2; index++ {
		loaded, loadErr := store.Get(ctx, secret)
		if loadErr != nil || loaded != grant {
			t.Fatalf("reusable preview load %d = %#v, %v", index, loaded, loadErr)
		}
	}
	if _, err := store.Get(ctx, strings.Repeat("c", 48)); err != ErrPreviewGrantExpired {
		t.Fatalf("unknown preview error = %v", err)
	}
}
