package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func exactTreeSearchAdmissionRequest(
	projectID string,
	actorID string,
	operation ExactTreeSearchAdmissionOperation,
) ExactTreeSearchAdmissionRequest {
	return ExactTreeSearchAdmissionRequest{
		ProjectID: projectID, ActorID: actorID, Operation: operation,
	}
}

func miniredisExactTreeSearchAdmission(
	t *testing.T,
	options RedisExactTreeSearchAdmissionOptions,
) (*miniredis.Miniredis, *redis.Client, *RedisExactTreeSearchAdmission) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = client.Close() })
	if options.Prefix == "" {
		options.Prefix = "worksflow:test:repository:search-admission:"
	}
	admission, err := NewRedisExactTreeSearchAdmission(client, options)
	if err != nil {
		t.Fatal(err)
	}
	return server, client, admission
}

func TestRedisExactTreeSearchAdmissionDefaultQueryLayersAndHashOnlyKeys(t *testing.T) {
	server, _, admission := miniredisExactTreeSearchAdmission(
		t, RedisExactTreeSearchAdmissionOptions{},
	)
	projectID, actorID := uuid.NewString(), uuid.NewString()
	request := exactTreeSearchAdmissionRequest(
		projectID, actorID, ExactTreeSearchAdmissionQuery,
	)
	for index := 0; index < 8; index++ {
		if err := admission.Admit(context.Background(), request); err != nil {
			t.Fatalf("default actor query %d = %v", index+1, err)
		}
	}
	err := admission.Admit(context.Background(), request)
	var denial *ExactTreeSearchAdmissionDeniedError
	if !errors.Is(err, ErrExactTreeSearchAdmissionDenied) ||
		!errors.As(err, &denial) || denial.Operation != ExactTreeSearchAdmissionQuery ||
		denial.RetryAfter <= 0 || denial.RetryAfter > 250*time.Millisecond ||
		errors.Is(err, ErrExactTreeSearchAdmissionUnavailable) {
		t.Fatalf("typed query denial = %#v, %v", denial, err)
	}
	if strings.Contains(err.Error(), projectID) || strings.Contains(err.Error(), actorID) {
		t.Fatalf("denial exposed admission identity: %v", err)
	}

	keys := server.Keys()
	if len(keys) != 2 {
		t.Fatalf("query admission keys = %#v", keys)
	}
	assertExactTreeSearchAdmissionHashOnlyKeys(
		t, keys, admission.prefix, projectID, actorID, string(request.Operation),
	)
}

func TestRedisExactTreeSearchAdmissionDefaultFirstBuilderAndQueryAreIndependent(t *testing.T) {
	_, _, admission := miniredisExactTreeSearchAdmission(
		t, RedisExactTreeSearchAdmissionOptions{},
	)
	projectID, firstActor := uuid.NewString(), uuid.NewString()
	build := exactTreeSearchAdmissionRequest(
		projectID, firstActor, ExactTreeSearchAdmissionFirstBuilder,
	)
	if err := admission.Admit(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	err := admission.Admit(context.Background(), build)
	var actorDenial *ExactTreeSearchAdmissionDeniedError
	if !errors.As(err, &actorDenial) || actorDenial.RetryAfter <= 29*time.Second ||
		actorDenial.RetryAfter > 30*time.Second {
		t.Fatalf("default first-builder actor denial = %#v, %v", actorDenial, err)
	}
	query := build
	query.Operation = ExactTreeSearchAdmissionQuery
	if err := admission.Admit(context.Background(), query); err != nil {
		t.Fatalf("builder traffic consumed query budget: %v", err)
	}

	second := build
	second.ActorID = uuid.NewString()
	if err := admission.Admit(context.Background(), second); err != nil {
		t.Fatalf("second project builder token = %v", err)
	}
	third := build
	third.ActorID = uuid.NewString()
	err = admission.Admit(context.Background(), third)
	var projectDenial *ExactTreeSearchAdmissionDeniedError
	if !errors.As(err, &projectDenial) || projectDenial.RetryAfter <= 14*time.Second ||
		projectDenial.RetryAfter > 15*time.Second {
		t.Fatalf("default first-builder project denial = %#v, %v", projectDenial, err)
	}
}

func TestRedisExactTreeSearchAdmissionSupportsBoundedCustomLimits(t *testing.T) {
	server, _, admission := miniredisExactTreeSearchAdmission(t, RedisExactTreeSearchAdmissionOptions{
		Query: ExactTreeSearchAdmissionOperationLimits{
			Project: ExactTreeSearchAdmissionBucketLimits{
				RefillTokens: 1, RefillInterval: time.Second, Burst: 2,
			},
			Actor: ExactTreeSearchAdmissionBucketLimits{
				RefillTokens: 1, RefillInterval: time.Second, Burst: 2,
			},
		},
	})
	now := time.Unix(1_700_000_000, 0).UTC()
	server.SetTime(now)
	request := exactTreeSearchAdmissionRequest(
		uuid.NewString(), uuid.NewString(), ExactTreeSearchAdmissionQuery,
	)
	for index := 0; index < 2; index++ {
		if err := admission.Admit(context.Background(), request); err != nil {
			t.Fatalf("custom allow %d = %v", index+1, err)
		}
	}
	if err := admission.Admit(context.Background(), request); !errors.Is(err, ErrExactTreeSearchAdmissionDenied) {
		t.Fatalf("custom burst denial = %v", err)
	}
	// FastForward only advances miniredis TTLs. SetTime is the clock observed
	// by Redis TIME, which is deliberately the token bucket's sole clock.
	server.SetTime(now.Add(time.Second))
	if err := admission.Admit(context.Background(), request); err != nil {
		t.Fatalf("custom refill = %v", err)
	}
}

func TestRedisExactTreeSearchAdmissionFailsClosedOnCorruptState(t *testing.T) {
	server, _, admission := miniredisExactTreeSearchAdmission(
		t, RedisExactTreeSearchAdmissionOptions{},
	)
	request := exactTreeSearchAdmissionRequest(
		uuid.NewString(), uuid.NewString(), ExactTreeSearchAdmissionQuery,
	)
	if err := admission.Admit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	keys := server.Keys()
	if len(keys) != 2 {
		t.Fatalf("admission keys = %#v", keys)
	}
	server.HSet(keys[0], "tokens", "not-a-number")
	err := admission.Admit(context.Background(), request)
	if !errors.Is(err, ErrExactTreeSearchAdmissionUnavailable) ||
		errors.Is(err, ErrExactTreeSearchAdmissionDenied) {
		t.Fatalf("corrupt state was not fail-closed infrastructure error: %v", err)
	}
}

func TestRedisExactTreeSearchAdmissionRejectsInvalidContractAndRequest(t *testing.T) {
	if _, err := NewRedisExactTreeSearchAdmission(nil, RedisExactTreeSearchAdmissionOptions{}); !errors.Is(
		err, ErrExactTreeSearchAdmissionInvalid,
	) {
		t.Fatalf("nil Redis client = %v", err)
	}
	_, client, admission := miniredisExactTreeSearchAdmission(
		t, RedisExactTreeSearchAdmissionOptions{},
	)
	invalidOptions := []RedisExactTreeSearchAdmissionOptions{
		{Prefix: "unsafe:{tag}:"},
		{Timeout: maximumExactTreeSearchAdmissionTimeout + time.Millisecond},
		{Timeout: time.Microsecond},
		{Query: ExactTreeSearchAdmissionOperationLimits{
			Project: ExactTreeSearchAdmissionBucketLimits{
				RefillTokens: 1, RefillInterval: time.Second, Burst: 1,
			},
		}},
		{FirstBuilder: ExactTreeSearchAdmissionOperationLimits{
			Project: ExactTreeSearchAdmissionBucketLimits{
				RefillTokens: 1, RefillInterval: time.Hour, Burst: 100_000,
			},
			Actor: ExactTreeSearchAdmissionBucketLimits{
				RefillTokens: 1, RefillInterval: time.Hour, Burst: 100_000,
			},
		}},
	}
	for _, options := range invalidOptions {
		if _, err := NewRedisExactTreeSearchAdmission(client, options); !errors.Is(
			err, ErrExactTreeSearchAdmissionInvalid,
		) {
			t.Fatalf("invalid options %#v = %v", options, err)
		}
	}
	projectID, actorID := uuid.NewString(), uuid.NewString()
	invalidRequests := []ExactTreeSearchAdmissionRequest{
		exactTreeSearchAdmissionRequest(strings.ToUpper(projectID), actorID, ExactTreeSearchAdmissionQuery),
		exactTreeSearchAdmissionRequest(projectID, "not-an-actor", ExactTreeSearchAdmissionQuery),
		exactTreeSearchAdmissionRequest(projectID, actorID, "unknown"),
	}
	for _, request := range invalidRequests {
		if err := admission.Admit(context.Background(), request); !errors.Is(
			err, ErrExactTreeSearchAdmissionInvalid,
		) {
			t.Fatalf("invalid request %#v = %v", request, err)
		}
	}
}

func TestRedisExactTreeSearchAdmissionTimeoutIsBoundedAndFailClosed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
			_, _ = io.Copy(io.Discard, connection)
		}
	}()
	client := redis.NewClient(&redis.Options{
		Addr: listener.Addr().String(), MaxRetries: -1,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
		// Leave ContextTimeoutEnabled false to prove the admission boundary's
		// deadline does not depend on the shared client's timeout policy.
	})
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case connection := <-accepted:
			_ = connection.Close()
		default:
		}
	})
	admission, err := NewRedisExactTreeSearchAdmission(
		client, RedisExactTreeSearchAdmissionOptions{Timeout: 40 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = admission.Admit(context.Background(), exactTreeSearchAdmissionRequest(
		uuid.NewString(), uuid.NewString(), ExactTreeSearchAdmissionQuery,
	))
	elapsed := time.Since(started)
	if !errors.Is(err, ErrExactTreeSearchAdmissionUnavailable) ||
		errors.Is(err, ErrExactTreeSearchAdmissionDenied) || elapsed > 250*time.Millisecond {
		t.Fatalf("bounded fail-closed timeout = %v after %s", err, elapsed)
	}
}

func assertExactTreeSearchAdmissionHashOnlyKeys(
	t *testing.T,
	keys []string,
	prefix string,
	forbidden ...string,
) {
	t.Helper()
	projectTag := ""
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix+"{") {
			t.Fatalf("admission key prefix/hash tag = %q", key)
		}
		remainder := strings.TrimPrefix(key, prefix+"{")
		parts := strings.Split(remainder, "}:")
		if len(parts) != 2 {
			t.Fatalf("admission key shape = %q", key)
		}
		for _, encoded := range parts {
			decoded, err := hex.DecodeString(encoded)
			if err != nil || len(decoded) != 32 {
				t.Fatalf("admission key contains a non-SHA-256 component: %q", key)
			}
		}
		if projectTag == "" {
			projectTag = parts[0]
		} else if projectTag != parts[0] {
			t.Fatalf("project admission keys do not share one hash tag: %#v", keys)
		}
		for _, raw := range forbidden {
			if strings.Contains(key, raw) {
				t.Fatalf("admission key exposed raw value %q: %q", raw, key)
			}
		}
	}
}
