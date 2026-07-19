package lsp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func lspRateRequest(method string) TicketRateLimitRequest {
	return TicketRateLimitRequest{
		TenantID: testProject, ProjectID: testProject, ActorID: testActor,
		SessionID: testSession, ProfileIDs: []string{"typescript"}, Method: method,
	}
}

func TestRedisTicketRateLimiterEnforcesAtomicLayeredBurstWithoutRawIdentifiers(t *testing.T) {
	client, prefix := lspRedisCanary(t)
	limiter, err := NewRedisTicketRateLimiter(client, RedisTicketRateLimiterOptions{
		Prefix: prefix + "rate:", RefillPerSecond: 1, Burst: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := lspRateRequest(TicketAuditIssue)
	for index := 0; index < 2; index++ {
		decision, allowErr := limiter.Allow(context.Background(), request)
		if allowErr != nil || !decision.Allowed {
			t.Fatalf("allow %d = %#v, %v", index, decision, allowErr)
		}
	}
	decision, err := limiter.Allow(context.Background(), request)
	if err != nil || decision.Allowed || decision.RetryAfter <= 0 || decision.RetryAfter > time.Second {
		t.Fatalf("burst rejection = %#v, %v", decision, err)
	}
	keys, err := client.Keys(context.Background(), prefix+"rate:*").Result()
	if err != nil || len(keys) != 6 {
		t.Fatalf("layered keys = %#v, %v", keys, err)
	}
	for _, key := range keys {
		if strings.Contains(key, testProject) || strings.Contains(key, testActor) ||
			strings.Contains(key, testSession) || strings.Contains(key, "typescript") {
			t.Fatalf("rate key exposed authority identity: %q", key)
		}
	}
}

func TestRedisTicketRateLimiterConcurrentBurstHasExactBound(t *testing.T) {
	client, prefix := lspRedisCanary(t)
	limiter, err := NewRedisTicketRateLimiter(client, RedisTicketRateLimiterOptions{
		Prefix: prefix + "concurrent-rate:", RefillPerSecond: 1, Burst: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 32
	var group sync.WaitGroup
	results := make(chan bool, callers)
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			decision, allowErr := limiter.Allow(context.Background(), lspRateRequest(TicketAuditConsume))
			results <- allowErr == nil && decision.Allowed
		}()
	}
	group.Wait()
	close(results)
	allowed := 0
	for result := range results {
		if result {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("concurrent allowed = %d, want 10", allowed)
	}
}

func TestRedisTicketRateLimiterFailsClosedDuringOutage(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", MaxRetries: -1,
		DialTimeout: 25 * time.Millisecond, ReadTimeout: 25 * time.Millisecond,
		WriteTimeout: 25 * time.Millisecond, PoolTimeout: 25 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	limiter, err := NewRedisTicketRateLimiter(client, RedisTicketRateLimiterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.Allow(context.Background(), lspRateRequest(TicketAuditIssue)); !errors.Is(err, ErrTicketUnavailable) {
		t.Fatalf("outage error = %v", err)
	}
}
