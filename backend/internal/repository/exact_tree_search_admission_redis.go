package repository

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

const (
	defaultExactTreeSearchAdmissionPrefix  = "worksflow:repository:exact-tree-search-admission:"
	defaultExactTreeSearchAdmissionTimeout = 250 * time.Millisecond
	maximumExactTreeSearchAdmissionTimeout = 250 * time.Millisecond
)

type ExactTreeSearchAdmissionBucketLimits struct {
	RefillTokens   int64
	RefillInterval time.Duration
	Burst          int64
}

type ExactTreeSearchAdmissionOperationLimits struct {
	Project ExactTreeSearchAdmissionBucketLimits
	Actor   ExactTreeSearchAdmissionBucketLimits
}

type RedisExactTreeSearchAdmissionOptions struct {
	Prefix       string
	Timeout      time.Duration
	Query        ExactTreeSearchAdmissionOperationLimits
	FirstBuilder ExactTreeSearchAdmissionOperationLimits
}

type RedisExactTreeSearchAdmission struct {
	client       redis.UniversalClient
	prefix       string
	timeout      time.Duration
	query        ExactTreeSearchAdmissionOperationLimits
	firstBuilder ExactTreeSearchAdmissionOperationLimits
}

func NewRedisExactTreeSearchAdmission(
	client redis.UniversalClient,
	options RedisExactTreeSearchAdmissionOptions,
) (*RedisExactTreeSearchAdmission, error) {
	if client == nil {
		return nil, ErrExactTreeSearchAdmissionInvalid
	}
	if options.Prefix == "" {
		options.Prefix = defaultExactTreeSearchAdmissionPrefix
	}
	if options.Prefix != strings.TrimSpace(options.Prefix) || len(options.Prefix) > 200 ||
		strings.ContainsAny(options.Prefix, "{}\r\n\x00") {
		return nil, ErrExactTreeSearchAdmissionInvalid
	}
	if options.Timeout == 0 {
		options.Timeout = defaultExactTreeSearchAdmissionTimeout
	}
	if options.Timeout < time.Millisecond || options.Timeout > maximumExactTreeSearchAdmissionTimeout ||
		options.Timeout%time.Millisecond != 0 {
		return nil, ErrExactTreeSearchAdmissionInvalid
	}

	queryDefaults := ExactTreeSearchAdmissionOperationLimits{
		Project: ExactTreeSearchAdmissionBucketLimits{
			RefillTokens: 20, RefillInterval: time.Second, Burst: 40,
		},
		Actor: ExactTreeSearchAdmissionBucketLimits{
			RefillTokens: 4, RefillInterval: time.Second, Burst: 8,
		},
	}
	firstBuilderDefaults := ExactTreeSearchAdmissionOperationLimits{
		Project: ExactTreeSearchAdmissionBucketLimits{
			RefillTokens: 1, RefillInterval: 15 * time.Second, Burst: 2,
		},
		Actor: ExactTreeSearchAdmissionBucketLimits{
			RefillTokens: 1, RefillInterval: 30 * time.Second, Burst: 1,
		},
	}
	query, err := normalizeExactTreeSearchAdmissionLimits(options.Query, queryDefaults)
	if err != nil {
		return nil, err
	}
	firstBuilder, err := normalizeExactTreeSearchAdmissionLimits(
		options.FirstBuilder, firstBuilderDefaults,
	)
	if err != nil {
		return nil, err
	}
	return &RedisExactTreeSearchAdmission{
		client: client, prefix: options.Prefix, timeout: options.Timeout,
		query: query, firstBuilder: firstBuilder,
	}, nil
}

var exactTreeSearchAdmissionScript = redis.NewScript(`
if #KEYS ~= 2 or #ARGV ~= 6 then
  return redis.error_reply('invalid exact-tree search admission contract')
end

local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local allowed = 1
local retry_ms = 0
local states = {}

for index, key in ipairs(KEYS) do
  local offset = (index - 1) * 3
  local refill_tokens = tonumber(ARGV[offset + 1])
  local refill_interval_ms = tonumber(ARGV[offset + 2])
  local burst = tonumber(ARGV[offset + 3])
  if refill_tokens == nil or refill_tokens <= 0 or refill_interval_ms == nil or
     refill_interval_ms <= 0 or burst == nil or burst < 1 or refill_tokens > burst then
    return redis.error_reply('invalid exact-tree search admission limits')
  end

  local tokens = burst
  local observed_at_ms = now_ms
  if redis.call('EXISTS', key) == 1 then
    local value = redis.call('HMGET', key, 'tokens', 'observed_at_ms')
    tokens = tonumber(value[1])
    observed_at_ms = tonumber(value[2])
    if tokens == nil or observed_at_ms == nil or tokens < 0 or tokens > burst or
       observed_at_ms > now_ms then
      return redis.error_reply('invalid exact-tree search admission state')
    end
    local elapsed_ms = now_ms - observed_at_ms
    tokens = math.min(burst, tokens + (elapsed_ms * refill_tokens / refill_interval_ms))
  end

  if tokens < 1 then
    allowed = 0
    retry_ms = math.max(
      retry_ms,
      math.ceil((1 - tokens) * refill_interval_ms / refill_tokens)
    )
  end
  local fill_ms = math.ceil(burst * refill_interval_ms / refill_tokens)
  local expiry_ms = math.max(1000, fill_ms * 2)
  states[index] = {tokens, now_ms, expiry_ms}
end

for index, key in ipairs(KEYS) do
  local tokens = states[index][1]
  if allowed == 1 then
    tokens = tokens - 1
  end
  redis.call(
    'HSET', key,
    'tokens', tostring(tokens),
    'observed_at_ms', tostring(states[index][2])
  )
  redis.call('PEXPIRE', key, states[index][3])
end

return {allowed, retry_ms}
`)

func (admission *RedisExactTreeSearchAdmission) Admit(
	ctx context.Context,
	request ExactTreeSearchAdmissionRequest,
) error {
	if admission == nil || admission.client == nil || ctx == nil ||
		!canonicalExactTreeSearchAdmissionUUID(request.ProjectID) ||
		!canonicalExactTreeSearchAdmissionUUID(request.ActorID) ||
		(request.Operation != ExactTreeSearchAdmissionQuery &&
			request.Operation != ExactTreeSearchAdmissionFirstBuilder) {
		return ErrExactTreeSearchAdmissionInvalid
	}
	limits := admission.query
	if request.Operation == ExactTreeSearchAdmissionFirstBuilder {
		limits = admission.firstBuilder
	}
	keys := []string{
		admission.scopeKey(request.ProjectID, request.Operation, "project", ""),
		admission.scopeKey(request.ProjectID, request.Operation, "actor", request.ActorID),
	}
	arguments := []any{
		limits.Project.RefillTokens, limits.Project.RefillInterval.Milliseconds(), limits.Project.Burst,
		limits.Actor.RefillTokens, limits.Actor.RefillInterval.Milliseconds(), limits.Actor.Burst,
	}
	admissionCtx, cancelAdmission := context.WithTimeout(ctx, admission.timeout)
	defer cancelAdmission()
	result, err := runExactTreeSearchAdmissionScript(
		admissionCtx, admission.client, keys, arguments,
	)
	if err != nil {
		return errors.Join(ErrExactTreeSearchAdmissionUnavailable, err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return ErrExactTreeSearchAdmissionUnavailable
	}
	allowed, err := exactTreeSearchAdmissionRedisInteger(values[0])
	if err != nil || (allowed != 0 && allowed != 1) {
		return ErrExactTreeSearchAdmissionUnavailable
	}
	retryMilliseconds, err := exactTreeSearchAdmissionRedisInteger(values[1])
	if err != nil || retryMilliseconds < 0 ||
		retryMilliseconds > int64(maximumExactTreeSearchAdmissionRetry/time.Millisecond) {
		return ErrExactTreeSearchAdmissionUnavailable
	}
	if allowed == 1 {
		if retryMilliseconds != 0 {
			return ErrExactTreeSearchAdmissionUnavailable
		}
		return nil
	}
	if retryMilliseconds <= 0 {
		return ErrExactTreeSearchAdmissionUnavailable
	}
	return &ExactTreeSearchAdmissionDeniedError{
		Operation:  request.Operation,
		RetryAfter: time.Duration(retryMilliseconds) * time.Millisecond,
	}
}

type exactTreeSearchAdmissionScriptResult struct {
	value any
	err   error
}

func runExactTreeSearchAdmissionScript(
	ctx context.Context,
	client redis.UniversalClient,
	keys []string,
	arguments []any,
) (any, error) {
	// UniversalClient may have ContextTimeoutEnabled disabled. Keep the
	// admission boundary hard-bounded even in that configuration: a late Redis
	// success can only consume a token for a request that already failed closed,
	// which is conservative. The buffered result also lets the I/O goroutine
	// finish without waiting for a receiver after the deadline.
	result := make(chan exactTreeSearchAdmissionScriptResult, 1)
	go func() {
		value, err := exactTreeSearchAdmissionScript.Run(
			ctx, client, keys, arguments...,
		).Result()
		result <- exactTreeSearchAdmissionScriptResult{value: value, err: err}
	}()
	select {
	case completed := <-result:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return completed.value, completed.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (admission *RedisExactTreeSearchAdmission) scopeKey(
	projectID string,
	operation ExactTreeSearchAdmissionOperation,
	kind string,
	actorID string,
) string {
	projectDigest := exactTreeSearchAdmissionDigest(
		"exact-tree-search-admission-project/v1", projectID,
	)
	values := []string{
		"exact-tree-search-admission-scope/v1", string(operation), kind, projectID,
	}
	if actorID != "" {
		values = append(values, actorID)
	}
	scopeDigest := exactTreeSearchAdmissionDigest(values...)
	return fmt.Sprintf("%s{%x}:%x", admission.prefix, projectDigest, scopeDigest)
}

func normalizeExactTreeSearchAdmissionLimits(
	configured ExactTreeSearchAdmissionOperationLimits,
	defaults ExactTreeSearchAdmissionOperationLimits,
) (ExactTreeSearchAdmissionOperationLimits, error) {
	if zeroExactTreeSearchAdmissionBucket(configured.Project) &&
		zeroExactTreeSearchAdmissionBucket(configured.Actor) {
		return defaults, nil
	}
	if validateExactTreeSearchAdmissionBucket(configured.Project) != nil ||
		validateExactTreeSearchAdmissionBucket(configured.Actor) != nil {
		return ExactTreeSearchAdmissionOperationLimits{}, ErrExactTreeSearchAdmissionInvalid
	}
	return configured, nil
}

func zeroExactTreeSearchAdmissionBucket(limit ExactTreeSearchAdmissionBucketLimits) bool {
	return limit.RefillTokens == 0 && limit.RefillInterval == 0 && limit.Burst == 0
}

func validateExactTreeSearchAdmissionBucket(limit ExactTreeSearchAdmissionBucketLimits) error {
	if limit.RefillTokens < 1 || limit.RefillTokens > 10_000 ||
		limit.RefillInterval < 10*time.Millisecond || limit.RefillInterval > time.Hour ||
		limit.RefillInterval%time.Millisecond != 0 || limit.Burst < limit.RefillTokens ||
		limit.Burst > 100_000 {
		return ErrExactTreeSearchAdmissionInvalid
	}
	fillMilliseconds := limit.Burst * limit.RefillInterval.Milliseconds() / limit.RefillTokens
	if fillMilliseconds <= 0 || fillMilliseconds > int64((24*time.Hour)/time.Millisecond) {
		return ErrExactTreeSearchAdmissionInvalid
	}
	return nil
}

func canonicalExactTreeSearchAdmissionUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func exactTreeSearchAdmissionDigest(values ...string) [sha256.Size]byte {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(strconv.Itoa(len(value))))
		_, _ = digest.Write([]byte{':'})
		_, _ = digest.Write([]byte(value))
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func exactTreeSearchAdmissionRedisInteger(value any) (int64, error) {
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
	return 0, ErrExactTreeSearchAdmissionUnavailable
}

var _ ExactTreeSearchAdmission = (*RedisExactTreeSearchAdmission)(nil)
