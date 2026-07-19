package lsp

import (
	"context"
	"encoding/base64"
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

func lspRedisCanary(t *testing.T) (*redis.Client, string) {
	t.Helper()
	address := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDRESS"))
	if address == "" {
		address = strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDR"))
	}
	if address == "" {
		t.Skip("WORKSFLOW_TEST_REDIS_ADDRESS is required for the real Redis LSP ticket canary")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("connect Redis canary: %v", err)
	}
	prefix := "worksflow:test:sandbox:lsp-ticket:" + uuid.NewString() + ":"
	t.Cleanup(func() {
		keys, _ := client.Keys(context.Background(), prefix+"*").Result()
		if len(keys) != 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	})
	return client, prefix
}

func lspRedisSecret(seed byte) string {
	value := make([]byte, 32)
	for index := range value {
		value[index] = seed
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func lspRedisGrant(now time.Time) TicketGrant {
	return TicketGrant{
		SchemaVersion: TicketSchemaVersion,
		ID:            testTicket,
		ProjectID:     testProject,
		SessionID:     testSession,
		ActorID:       testActor,
		Origin:        "https://builder.example",
		Mode:          TicketModeSnapshot,
		Head:          validHead(),
		TemplateRelease: ExactTemplateRelease{
			ID: testRelease, ContentHash: lspDigest("2"),
		},
		Profiles:  []ProfileIdentity{lspTestProfile("typescript")},
		IssuedAt:  now,
		ExpiresAt: now.Add(30 * time.Second),
	}
}

func TestRedisTicketGrantStoreConsumesExactlyOnceConcurrentlyWithoutPlaintextKey(t *testing.T) {
	client, prefix := lspRedisCanary(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store, err := NewRedisTicketGrantStore(client, prefix, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	secret := lspRedisSecret(0x42)
	grant := lspRedisGrant(now)
	if err := store.Put(ctx, secret, grant, maxTicketGrantTTL); err != nil {
		t.Fatal(err)
	}
	keys, err := client.Keys(ctx, prefix+"*").Result()
	if err != nil || len(keys) != 1 || strings.Contains(keys[0], secret) {
		t.Fatalf("ticket key exposed plaintext or was not stored: keys=%#v err=%v", keys, err)
	}
	storedValue, err := client.Get(ctx, keys[0]).Result()
	if err != nil || strings.Contains(storedValue, secret) {
		t.Fatalf("ticket value exposed plaintext secret: err=%v", err)
	}

	const consumers = 32
	type result struct {
		grant TicketGrant
		err   error
	}
	results := make(chan result, consumers)
	var group sync.WaitGroup
	for index := 0; index < consumers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			loaded, consumeErr := store.Consume(ctx, secret)
			results <- result{grant: loaded, err: consumeErr}
		}()
	}
	group.Wait()
	close(results)
	successes, consumed := 0, 0
	for item := range results {
		switch {
		case item.err == nil:
			successes++
			if item.grant.ID != grant.ID || !item.grant.Head.Equal(grant.Head) {
				t.Fatalf("consumer loaded a different grant: %#v", item.grant)
			}
		case errors.Is(item.err, ErrTicketConsumed):
			consumed++
		default:
			t.Fatalf("consume failed with unsafe error mapping: %v", item.err)
		}
	}
	if successes != 1 || consumed != consumers-1 {
		t.Fatalf("atomic consume results: successes=%d consumed=%d", successes, consumed)
	}
	if remaining, err := client.Keys(ctx, prefix+"*").Result(); err != nil || len(remaining) != 0 {
		t.Fatalf("consumed ticket was retained: keys=%#v err=%v", remaining, err)
	}
}

func TestRedisTicketGrantStoreTreatsLogicalAndRedisExpiryAsConsumed(t *testing.T) {
	client, prefix := lspRedisCanary(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store, err := NewRedisTicketGrantStore(client, prefix, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	logicalSecret := lspRedisSecret(0x11)
	logicalGrant := lspRedisGrant(now)
	logicalGrant.ExpiresAt = now.Add(5 * time.Second)
	if err := store.Put(ctx, logicalSecret, logicalGrant, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	if _, err := store.Consume(ctx, logicalSecret); !errors.Is(err, ErrTicketConsumed) {
		t.Fatalf("logical expiry error = %v", err)
	}
	if exists, err := client.Exists(ctx, store.key(logicalSecret)).Result(); err != nil || exists != 0 {
		t.Fatalf("logically expired grant was not burned: exists=%d err=%v", exists, err)
	}

	now = time.Now().UTC().Truncate(time.Microsecond)
	redisSecret := lspRedisSecret(0x12)
	redisGrant := lspRedisGrant(now)
	if err := store.Put(ctx, redisSecret, redisGrant, maxTicketGrantTTL); err != nil {
		t.Fatal(err)
	}
	if err := client.PExpire(ctx, store.key(redisSecret), time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		exists, existsErr := client.Exists(ctx, store.key(redisSecret)).Result()
		if existsErr != nil {
			t.Fatal(existsErr)
		}
		if exists == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Redis did not expire the LSP ticket grant")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := store.Consume(ctx, redisSecret); !errors.Is(err, ErrTicketConsumed) {
		t.Fatalf("Redis expiry/missing error = %v", err)
	}
}

func TestRedisTicketGrantStoreRejectsAndBurnsNoncanonicalPersistedGrant(t *testing.T) {
	client, prefix := lspRedisCanary(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store, err := NewRedisTicketGrantStore(client, prefix, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	secret := lspRedisSecret(0x33)
	grant := lspRedisGrant(now)
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append(encoded[:len(encoded)-1], []byte(`,"unexpected":true}`)...)
	if err := client.Set(ctx, store.key(secret), corrupt, maxTicketGrantTTL).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(ctx, secret); !errors.Is(err, ErrTicketUnavailable) {
		t.Fatalf("noncanonical persisted grant error = %v", err)
	}
	if _, err := store.Consume(ctx, secret); !errors.Is(err, ErrTicketConsumed) {
		t.Fatalf("corrupt grant replay error = %v", err)
	}

	oversizedSecret := lspRedisSecret(0x34)
	if err := client.Set(ctx, store.key(oversizedSecret), strings.Repeat("x", maxTicketGrantBytes+1), maxTicketGrantTTL).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(ctx, oversizedSecret); !errors.Is(err, ErrTicketUnavailable) {
		t.Fatalf("oversized persisted grant error = %v", err)
	}
}

func TestRedisTicketGrantStoreFailsClosedDuringRedisOutage(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", MaxRetries: -1,
		DialTimeout: 25 * time.Millisecond, ReadTimeout: 25 * time.Millisecond,
		WriteTimeout: 25 * time.Millisecond, PoolTimeout: 25 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	store, err := NewRedisTicketGrantStore(client, "", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	secret := lspRedisSecret(0x55)
	grant := lspRedisGrant(now)
	if err := store.Put(context.Background(), secret, grant, maxTicketGrantTTL); !errors.Is(err, ErrTicketUnavailable) {
		t.Fatalf("outage Put error = %v", err)
	}
	if _, err := store.Consume(context.Background(), secret); !errors.Is(err, ErrTicketUnavailable) || errors.Is(err, ErrTicketConsumed) {
		t.Fatalf("outage Consume error = %v", err)
	}
	if err := store.Put(context.Background(), secret, grant, maxTicketGrantTTL+time.Nanosecond); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("overlong TTL error = %v", err)
	}
}
