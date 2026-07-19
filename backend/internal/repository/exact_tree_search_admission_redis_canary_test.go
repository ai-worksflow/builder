package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisExactTreeSearchAdmissionRealRedisConcurrentBurstIsAtomic(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDRESS"))
	if address == "" {
		address = strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_REDIS_ADDR"))
	}
	if address == "" {
		t.Skip("WORKSFLOW_TEST_REDIS_ADDRESS is required for the exact-tree search admission canary")
	}
	client := redis.NewClient(&redis.Options{
		Addr: address, PoolSize: 64, ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	prefix := "worksflow:test:repository:search-admission:" + uuid.NewString() + ":"
	t.Cleanup(func() {
		keys, _ := client.Keys(context.Background(), prefix+"*").Result()
		if len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	})
	limits := ExactTreeSearchAdmissionOperationLimits{
		Project: ExactTreeSearchAdmissionBucketLimits{
			RefillTokens: 1, RefillInterval: time.Hour, Burst: 12,
		},
		Actor: ExactTreeSearchAdmissionBucketLimits{
			RefillTokens: 1, RefillInterval: time.Hour, Burst: 12,
		},
	}
	admission, err := NewRedisExactTreeSearchAdmission(client, RedisExactTreeSearchAdmissionOptions{
		Prefix: prefix, Query: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := exactTreeSearchAdmissionRequest(
		uuid.NewString(), uuid.NewString(), ExactTreeSearchAdmissionQuery,
	)

	const callers = 64
	var group sync.WaitGroup
	results := make(chan error, callers)
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- admission.Admit(context.Background(), request)
		}()
	}
	group.Wait()
	close(results)
	allowed, denied := 0, 0
	for result := range results {
		switch {
		case result == nil:
			allowed++
		case errors.Is(result, ErrExactTreeSearchAdmissionDenied):
			var denial *ExactTreeSearchAdmissionDeniedError
			if !errors.As(result, &denial) || denial.RetryAfter <= 0 ||
				denial.RetryAfter > time.Hour {
				t.Fatalf("real Redis denial = %#v, %v", denial, result)
			}
			denied++
		default:
			t.Fatalf("real Redis infrastructure error: %v", result)
		}
	}
	if allowed != 12 || denied != callers-12 {
		t.Fatalf("real Redis concurrent admission: allowed=%d denied=%d", allowed, denied)
	}
	keys, err := client.Keys(context.Background(), prefix+"*").Result()
	if err != nil || len(keys) != 2 {
		t.Fatalf("real Redis admission keys = %#v, %v", keys, err)
	}
	assertExactTreeSearchAdmissionHashOnlyKeys(
		t, keys, prefix, request.ProjectID, request.ActorID, string(request.Operation),
	)
}
