package dataruntime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisPublicRateLimiterIsFailClosedAndSeparatesReadWriteBudgets(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("WORKSFLOW_TEST_REDIS_ADDR is not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	limiter, err := NewRedisPublicRateLimiter(client, RedisPublicRateLimiterOptions{
		Prefix: "worksflow:test:public-data:" + uuid.NewString() + ":",
		Window: 2 * time.Second, ReadLimit: 2, WriteLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilityID := uuid.NewString()
	read := PublicRateLimitRequest{CapabilityID: capabilityID, ClientKey: "192.0.2.10", Operation: PublicOperationRead}
	for index := 0; index < 2; index++ {
		decision, err := limiter.Allow(ctx, read)
		if err != nil || !decision.Allowed {
			t.Fatalf("read %d unexpectedly rejected: decision=%+v err=%v", index+1, decision, err)
		}
	}
	decision, err := limiter.Allow(ctx, read)
	if err != nil || decision.Allowed || decision.RetryAfter <= 0 {
		t.Fatalf("read budget did not close: decision=%+v err=%v", decision, err)
	}
	write := read
	write.Operation = PublicOperationCreate
	decision, err = limiter.Allow(ctx, write)
	if err != nil || !decision.Allowed {
		t.Fatalf("read traffic consumed the separate write budget: decision=%+v err=%v", decision, err)
	}
	decision, err = limiter.Allow(ctx, write)
	if err != nil || decision.Allowed {
		t.Fatalf("write budget did not close: decision=%+v err=%v", decision, err)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.Allow(ctx, read); err == nil {
		t.Fatal("unavailable Redis was treated as an allowed request")
	}
}

func TestRedisPublicRateLimiterRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := NewRedisPublicRateLimiter(nil, RedisPublicRateLimiterOptions{}); err == nil {
		t.Fatal("missing Redis client was accepted")
	}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer client.Close()
	if _, err := NewRedisPublicRateLimiter(client, RedisPublicRateLimiterOptions{Window: 24 * time.Hour}); err == nil {
		t.Fatal("unbounded rate window was accepted")
	}
}
