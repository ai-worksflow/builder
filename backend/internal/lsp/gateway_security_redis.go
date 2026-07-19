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
	"github.com/worksflow/builder/backend/internal/templates"
)

const defaultGatewayRequestRatePrefix = "worksflow:sandbox:lsp-request-rate:"

type RedisGatewayRequestRateLimiter struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisGatewayRequestRateLimiter(
	client redis.UniversalClient,
	prefix string,
) (*RedisGatewayRequestRateLimiter, error) {
	if client == nil {
		return nil, ErrGatewaySecurityUnavailable
	}
	if prefix == "" {
		prefix = defaultGatewayRequestRatePrefix
	}
	if strings.TrimSpace(prefix) != prefix || len(prefix) > 200 ||
		strings.ContainsAny(prefix, "\r\n\x00") {
		return nil, ErrGatewaySecurityUnavailable
	}
	return &RedisGatewayRequestRateLimiter{client: client, prefix: prefix}, nil
}

var gatewayRequestRateScript = redis.NewScript(`
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local allowed = 1
local retry_ms = 0
local states = {}

for index, key in ipairs(KEYS) do
  local refill = tonumber(ARGV[(index - 1) * 2 + 1])
  local burst = tonumber(ARGV[(index - 1) * 2 + 2])
  local expiry_ms = math.max(1000, math.ceil((burst / refill) * 2000))
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
  states[index] = {tokens, now_ms, expiry_ms}
end

for index, key in ipairs(KEYS) do
  local tokens = states[index][1]
  if allowed == 1 then
    tokens = tokens - 1
  end
  redis.call('HSET', key, 'tokens', tostring(tokens), 'observed_at_ms', tostring(now_ms))
  redis.call('PEXPIRE', key, states[index][3])
end

return {allowed, retry_ms}
`)

func (limiter *RedisGatewayRequestRateLimiter) AllowGatewayRequest(
	ctx context.Context,
	input GatewayRequestRateLimitInput,
) (GatewayRequestRateLimitDecision, error) {
	if limiter == nil || limiter.client == nil || ctx == nil || validateGatewayRateInput(input) != nil {
		return GatewayRequestRateLimitDecision{}, ErrGatewaySecurityUnavailable
	}
	keys := []string{
		limiter.scopeKey("project", input.ProjectID),
		limiter.scopeKey("actor", input.ProjectID, input.ActorID),
		limiter.scopeKey("session", input.ProjectID, input.SessionID),
		limiter.scopeKey(
			"profile", input.ProjectID, input.ProfileID, input.ProfileContentHash, input.CapabilityHash,
		),
		limiter.scopeKey(
			"method", input.ProjectID, input.ProfileID, input.ProfileContentHash,
			input.CapabilityHash, input.Method,
		),
	}
	arguments := make([]any, 0, len(keys)*2)
	for index := range keys {
		refill, burst := templates.LanguageServerMaxRequestsPerSecond, templates.LanguageServerMaxRequestBurst
		if index >= 3 {
			refill, burst = input.RequestsPerSecond, input.RequestBurst
		}
		arguments = append(arguments, refill, burst)
	}
	result, err := gatewayRequestRateScript.Run(ctx, limiter.client, keys, arguments...).Result()
	if err != nil {
		return GatewayRequestRateLimitDecision{}, errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return GatewayRequestRateLimitDecision{}, ErrGatewaySecurityUnavailable
	}
	allowed, err := gatewayRateInteger(values[0])
	if err != nil || allowed != 0 && allowed != 1 {
		return GatewayRequestRateLimitDecision{}, ErrGatewaySecurityUnavailable
	}
	retryMilliseconds, err := gatewayRateInteger(values[1])
	if err != nil || retryMilliseconds < 0 || retryMilliseconds > int64(time.Hour/time.Millisecond) {
		return GatewayRequestRateLimitDecision{}, ErrGatewaySecurityUnavailable
	}
	decision := GatewayRequestRateLimitDecision{
		Allowed: allowed == 1, RetryAfter: time.Duration(retryMilliseconds) * time.Millisecond,
	}
	if !decision.Allowed && decision.RetryAfter <= 0 {
		return GatewayRequestRateLimitDecision{}, ErrGatewaySecurityUnavailable
	}
	return decision, nil
}

// Every suffix is one SHA-256 digest. Scope kind and all identifiers are
// length-framed inside the digest, so neither authority IDs nor method/profile
// names appear in Redis keys.
func (limiter *RedisGatewayRequestRateLimiter) scopeKey(kind string, values ...string) string {
	digest := sha256.New()
	for _, value := range append([]string{kind}, values...) {
		_, _ = digest.Write([]byte(strconv.Itoa(len(value))))
		_, _ = digest.Write([]byte{':'})
		_, _ = digest.Write([]byte(value))
	}
	return fmt.Sprintf("%s{lsp-request-rate}:%x", limiter.prefix, digest.Sum(nil))
}

func gatewayRateInteger(value any) (int64, error) {
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
	return 0, ErrGatewaySecurityUnavailable
}

var _ GatewayRequestRateLimiter = (*RedisGatewayRequestRateLimiter)(nil)
