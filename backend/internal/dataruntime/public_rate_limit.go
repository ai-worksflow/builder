package dataruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type PublicRateLimitRequest struct {
	CapabilityID string
	ClientKey    string
	Operation    PublicDataOperation
}

type PublicRateLimitDecision struct {
	Allowed    bool
	Limit      int64
	Remaining  int64
	RetryAfter time.Duration
}

type PublicRateLimiter interface {
	Allow(context.Context, PublicRateLimitRequest) (PublicRateLimitDecision, error)
}

type RedisPublicRateLimiterOptions struct {
	Prefix     string
	Window     time.Duration
	ReadLimit  int64
	WriteLimit int64
}

type RedisPublicRateLimiter struct {
	client     redis.UniversalClient
	prefix     string
	window     time.Duration
	readLimit  int64
	writeLimit int64
}

func NewRedisPublicRateLimiter(client redis.UniversalClient, options RedisPublicRateLimiterOptions) (*RedisPublicRateLimiter, error) {
	if client == nil {
		return nil, errors.New("public data Redis rate limiter client is required")
	}
	if strings.TrimSpace(options.Prefix) == "" {
		options.Prefix = "worksflow:public-data:rate:"
	}
	if options.Window <= 0 {
		options.Window = time.Minute
	}
	if options.Window < time.Second || options.Window > time.Hour {
		return nil, errors.New("public data rate limit window must be between one second and one hour")
	}
	if options.ReadLimit <= 0 {
		options.ReadLimit = 180
	}
	if options.WriteLimit <= 0 {
		options.WriteLimit = 30
	}
	if options.ReadLimit > 100_000 || options.WriteLimit > 100_000 {
		return nil, errors.New("public data rate limit may not exceed 100000 requests per window")
	}
	return &RedisPublicRateLimiter{
		client: client, prefix: options.Prefix, window: options.Window,
		readLimit: options.ReadLimit, writeLimit: options.WriteLimit,
	}, nil
}

var publicRateLimitScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  ttl = tonumber(ARGV[1])
end
return {current, ttl}
`)

func (l *RedisPublicRateLimiter) Allow(ctx context.Context, request PublicRateLimitRequest) (PublicRateLimitDecision, error) {
	if _, err := uuid.Parse(request.CapabilityID); err != nil {
		return PublicRateLimitDecision{}, errors.New("public data rate limit capability ID must be a UUID")
	}
	clientKey := strings.TrimSpace(request.ClientKey)
	if clientKey == "" || len(clientKey) > 512 {
		return PublicRateLimitDecision{}, errors.New("public data rate limit client key is invalid")
	}
	limit := l.writeLimit
	kind := "write"
	if request.Operation == PublicOperationRead {
		limit = l.readLimit
		kind = "read"
	}
	digest := sha256.Sum256([]byte(clientKey))
	key := fmt.Sprintf("%s{%s}:%s:%x", l.prefix, request.CapabilityID, kind, digest[:12])
	result, err := publicRateLimitScript.Run(ctx, l.client, []string{key}, l.window.Milliseconds()).Result()
	if err != nil {
		return PublicRateLimitDecision{}, fmt.Errorf("apply public data rate limit: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return PublicRateLimitDecision{}, errors.New("public data rate limiter returned an invalid result")
	}
	current, err := redisInteger(values[0])
	if err != nil {
		return PublicRateLimitDecision{}, err
	}
	ttlMilliseconds, err := redisInteger(values[1])
	if err != nil {
		return PublicRateLimitDecision{}, err
	}
	remaining := limit - current
	if remaining < 0 {
		remaining = 0
	}
	return PublicRateLimitDecision{
		Allowed: current <= limit, Limit: limit, Remaining: remaining,
		RetryAfter: time.Duration(ttlMilliseconds) * time.Millisecond,
	}, nil
}

func redisInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err == nil {
			return parsed, nil
		}
	case []byte:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err == nil {
			return parsed, nil
		}
	}
	return 0, errors.New("public data rate limiter returned a non-integer result")
}
