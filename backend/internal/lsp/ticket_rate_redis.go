package lsp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultTicketRatePrefix = "worksflow:sandbox:lsp-rate:"

type RedisTicketRateLimiterOptions struct {
	Prefix          string
	RefillPerSecond int64
	Burst           int64
}

type RedisTicketRateLimiter struct {
	client          redis.UniversalClient
	prefix          string
	refillPerSecond int64
	burst           int64
}

func NewRedisTicketRateLimiter(
	client redis.UniversalClient,
	options RedisTicketRateLimiterOptions,
) (*RedisTicketRateLimiter, error) {
	if client == nil {
		return nil, errors.New("LSP Redis rate limiter client is required")
	}
	if options.Prefix == "" {
		options.Prefix = defaultTicketRatePrefix
	}
	if strings.TrimSpace(options.Prefix) != options.Prefix || len(options.Prefix) > 200 ||
		strings.ContainsAny(options.Prefix, "\r\n\x00") {
		return nil, errors.New("LSP Redis rate limiter prefix is invalid")
	}
	if options.RefillPerSecond == 0 {
		options.RefillPerSecond = 30
	}
	if options.Burst == 0 {
		options.Burst = 60
	}
	if options.RefillPerSecond < 1 || options.RefillPerSecond > 10_000 ||
		options.Burst < options.RefillPerSecond || options.Burst > 20_000 {
		return nil, errors.New("LSP Redis rate limiter refill or burst is invalid")
	}
	return &RedisTicketRateLimiter{
		client: client, prefix: options.Prefix,
		refillPerSecond: options.RefillPerSecond, burst: options.Burst,
	}, nil
}

var ticketRateLimitScript = redis.NewScript(`
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local refill = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local expiry_ms = math.max(1000, math.ceil((burst / refill) * 2000))
local allowed = 1
local retry_ms = 0
local states = {}

for index, key in ipairs(KEYS) do
  local value = redis.call('HMGET', key, 'tokens', 'observed_at_ms')
  local tokens = tonumber(value[1])
  local observed_at_ms = tonumber(value[2])
  if tokens == nil or observed_at_ms == nil or observed_at_ms > now_ms then
    tokens = burst
    observed_at_ms = now_ms
  else
    local elapsed_ms = now_ms - observed_at_ms
    tokens = math.min(burst, tokens + (elapsed_ms * refill / 1000))
  end
  if tokens < 1 then
    allowed = 0
    retry_ms = math.max(retry_ms, math.ceil((1 - tokens) * 1000 / refill))
  end
  states[index] = {tokens, now_ms}
end

for index, key in ipairs(KEYS) do
  local tokens = states[index][1]
  if allowed == 1 then
    tokens = tokens - 1
  end
  redis.call('HSET', key, 'tokens', tostring(tokens), 'observed_at_ms', tostring(now_ms))
  redis.call('PEXPIRE', key, expiry_ms)
end

return {allowed, retry_ms}
`)

func (limiter *RedisTicketRateLimiter) Allow(
	ctx context.Context,
	request TicketRateLimitRequest,
) (TicketRateLimitDecision, error) {
	if limiter == nil || ctx == nil ||
		!canonicalUUID(request.TenantID) || !canonicalUUID(request.ProjectID) ||
		!canonicalUUID(request.ActorID) || !canonicalUUID(request.SessionID) ||
		(request.Method != TicketAuditIssue && request.Method != TicketAuditConsume) {
		return TicketRateLimitDecision{}, ErrTicketInvalid
	}
	profileIDs, err := validateRequestedProfiles(request.ProfileIDs)
	if err != nil {
		return TicketRateLimitDecision{}, err
	}
	keys := []string{
		limiter.scopeKey("tenant", request.TenantID),
		limiter.scopeKey("project", request.TenantID, request.ProjectID),
		limiter.scopeKey("actor", request.TenantID, request.ProjectID, request.ActorID),
		limiter.scopeKey("session", request.TenantID, request.ProjectID, request.SessionID),
		limiter.scopeKey("method", request.TenantID, request.ProjectID, request.Method),
	}
	for _, profileID := range profileIDs {
		keys = append(keys, limiter.scopeKey("profile", request.TenantID, request.ProjectID, profileID))
	}
	result, err := ticketRateLimitScript.Run(
		ctx, limiter.client, keys, limiter.refillPerSecond, limiter.burst,
	).Result()
	if err != nil {
		return TicketRateLimitDecision{}, fmt.Errorf("%w: apply Redis admission: %v", ErrTicketUnavailable, err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return TicketRateLimitDecision{}, ErrTicketUnavailable
	}
	allowed, err := ticketRateInteger(values[0])
	if err != nil || (allowed != 0 && allowed != 1) {
		return TicketRateLimitDecision{}, ErrTicketUnavailable
	}
	retryMilliseconds, err := ticketRateInteger(values[1])
	if err != nil || retryMilliseconds < 0 || retryMilliseconds > int64(time.Hour/time.Millisecond) {
		return TicketRateLimitDecision{}, ErrTicketUnavailable
	}
	return TicketRateLimitDecision{
		Allowed: allowed == 1, RetryAfter: time.Duration(retryMilliseconds) * time.Millisecond,
	}, nil
}

func (limiter *RedisTicketRateLimiter) scopeKey(kind string, values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(strconv.Itoa(len(value))))
		_, _ = digest.Write([]byte{':'})
		_, _ = digest.Write([]byte(value))
	}
	return fmt.Sprintf("%s{lsp-rate}:%s:%x", limiter.prefix, kind, digest.Sum(nil)[:16])
}

func ticketRateInteger(value any) (int64, error) {
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
	return 0, errors.New("LSP Redis rate limiter returned a non-integer result")
}

var _ TicketRateLimiter = (*RedisTicketRateLimiter)(nil)
