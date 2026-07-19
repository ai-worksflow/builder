package lsp

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func gatewayRateInput(profile ProfileIdentity) GatewayRequestRateLimitInput {
	return GatewayRequestRateLimitInput{
		ProjectID: testProject, ActorID: testActor, SessionID: testSession,
		ProfileID: profile.ID, ProfileContentHash: profile.ContentHash,
		CapabilityHash: profile.CapabilityHash, Method: "textDocument/hover",
		RequestsPerSecond: 1, RequestBurst: 2,
	}
}

func TestRedisGatewayRequestRateLimiterEnforcesExactLayeredBurstWithHashOnlyKeys(t *testing.T) {
	client, rootPrefix := lspRedisCanary(t)
	prefix := rootPrefix + "request-rate:"
	limiter, err := NewRedisGatewayRequestRateLimiter(client, prefix)
	if err != nil {
		t.Fatal(err)
	}
	profile := lspTestProfile("typescript")
	input := gatewayRateInput(profile)
	for index := 0; index < input.RequestBurst; index++ {
		decision, allowErr := limiter.AllowGatewayRequest(context.Background(), input)
		if allowErr != nil || !decision.Allowed || decision.RetryAfter != 0 {
			t.Fatalf("allow %d = %#v, %v", index, decision, allowErr)
		}
	}
	decision, err := limiter.AllowGatewayRequest(context.Background(), input)
	if err != nil || decision.Allowed || decision.RetryAfter <= 0 || decision.RetryAfter > time.Second {
		t.Fatalf("burst rejection = %#v, %v", decision, err)
	}
	keys, err := client.Keys(context.Background(), prefix+"*").Result()
	if err != nil || len(keys) != 5 {
		t.Fatalf("layered request keys = %#v, %v", keys, err)
	}
	keyPrefix := prefix + "{lsp-request-rate}:"
	for _, key := range keys {
		suffix := strings.TrimPrefix(key, keyPrefix)
		decoded, decodeErr := hex.DecodeString(suffix)
		if !strings.HasPrefix(key, keyPrefix) || decodeErr != nil || len(decoded) != 32 {
			t.Fatalf("request key is not a hash-only authority suffix: %q", key)
		}
		for _, raw := range []string{
			input.ProjectID, input.ActorID, input.SessionID, input.ProfileID,
			input.ProfileContentHash, input.CapabilityHash, input.Method,
		} {
			if strings.Contains(key, raw) {
				t.Fatalf("request key exposed raw authority identity %q: %q", raw, key)
			}
		}
	}
}

func TestRedisGatewayRequestRateLimiterConcurrentBurstIsAtomic(t *testing.T) {
	client, rootPrefix := lspRedisCanary(t)
	limiter, err := NewRedisGatewayRequestRateLimiter(client, rootPrefix+"request-concurrent:")
	if err != nil {
		t.Fatal(err)
	}
	input := gatewayRateInput(lspTestProfile("typescript"))
	input.RequestBurst = 10
	const callers = 32
	var group sync.WaitGroup
	results := make(chan bool, callers)
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			decision, allowErr := limiter.AllowGatewayRequest(context.Background(), input)
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
	if allowed != input.RequestBurst {
		t.Fatalf("concurrent allowed = %d, want %d", allowed, input.RequestBurst)
	}
}

func TestRedisGatewayRequestRateLimiterKeepsSharedScopesStableAcrossProfiles(t *testing.T) {
	client, rootPrefix := lspRedisCanary(t)
	limiter, err := NewRedisGatewayRequestRateLimiter(client, rootPrefix+"request-cross-profile:")
	if err != nil {
		t.Fatal(err)
	}
	first := gatewayRateInput(lspTestProfile("typescript"))
	for index := 0; index < first.RequestBurst; index++ {
		decision, allowErr := limiter.AllowGatewayRequest(context.Background(), first)
		if allowErr != nil || !decision.Allowed {
			t.Fatalf("first profile allow %d = %#v, %v", index, decision, allowErr)
		}
	}
	secondProfile := lspTestProfile("typescript-alt")
	second := gatewayRateInput(secondProfile)
	decision, err := limiter.AllowGatewayRequest(context.Background(), second)
	if err != nil || !decision.Allowed {
		t.Fatalf("second exact profile inherited another profile's bucket: %#v, %v", decision, err)
	}
	keys, err := client.Keys(context.Background(), rootPrefix+"request-cross-profile:*").Result()
	if err != nil || len(keys) != 7 {
		// Three shared scopes plus two exact profile/method scopes per profile.
		t.Fatalf("cross-profile layered keys = %#v, %v", keys, err)
	}
}

func TestRedisGatewayRequestRateLimiterFailsClosedDuringOutage(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", MaxRetries: -1,
		DialTimeout: 25 * time.Millisecond, ReadTimeout: 25 * time.Millisecond,
		WriteTimeout: 25 * time.Millisecond, PoolTimeout: 25 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	limiter, err := NewRedisGatewayRequestRateLimiter(client, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = limiter.AllowGatewayRequest(
		context.Background(), gatewayRateInput(lspTestProfile("typescript")),
	)
	if !errors.Is(err, ErrGatewaySecurityUnavailable) {
		t.Fatalf("outage error = %v", err)
	}
}
