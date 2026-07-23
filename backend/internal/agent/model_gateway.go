package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const modelCapabilitySchemaVersion = "agent-model-capability/v1"

var (
	ErrModelCapability           = errors.New("Agent model capability is invalid or unavailable")
	ErrModelCapabilityBusy       = errors.New("Agent model capability already has an active request")
	ErrModelInputBudgetExceeded  = errors.New("Agent model input budget is exhausted")
	ErrModelOutputBudgetExceeded = errors.New("Agent model output budget is exhausted")
	ErrModelGateway              = errors.New("Agent Model Gateway rejected the request")
)

type ModelCapabilityRecord struct {
	SchemaVersion   string    `json:"schemaVersion"`
	ID              string    `json:"id"`
	AttemptID       string    `json:"attemptId"`
	ProjectID       string    `json:"projectId"`
	Model           string    `json:"model"`
	MaxInputTokens  int64     `json:"maxInputTokens"`
	MaxOutputTokens int64     `json:"maxOutputTokens"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type ModelGatewayCapabilities interface {
	Authenticate(context.Context, string) (ModelCapabilityRecord, error)
	Acquire(context.Context, ModelCapabilityRecord, time.Duration, int64, int64) (ModelBudgetLease, error)
	Release(context.Context, ModelCapabilityRecord, ModelBudgetLease, int64, int64, bool) error
}

type ModelBudgetLease struct {
	ID                   string
	InputTokenUpperBound int64
	OutputTokenAllowance int64
}

type RedisModelCapabilityAuthority struct {
	client  *redis.Client
	prefix  string
	baseURL string
	now     func() time.Time
}

func NewRedisModelCapabilityAuthority(
	client *redis.Client,
	prefix, baseURL string,
) (*RedisModelCapabilityAuthority, error) {
	prefix = strings.TrimSpace(prefix)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if client == nil || prefix == "" || len(prefix) > 200 || strings.ContainsAny(prefix, "\r\n\x00") ||
		err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != "/internal/agent-model/v1" {
		return nil, fmt.Errorf("%w: Redis authority prefix or internal gateway base URL", ErrModelCapability)
	}
	return &RedisModelCapabilityAuthority{client: client, prefix: prefix, baseURL: baseURL, now: time.Now}, nil
}

func (authority *RedisModelCapabilityAuthority) Issue(
	ctx context.Context,
	attempt AgentAttempt,
	capsule TaskCapsule,
) (ModelCapability, error) {
	if authority == nil || ctx == nil || attempt.State != AttemptRunning || attempt.Lease == nil ||
		attempt.TaskCapsule != capsule.ExactReference() || attempt.ProjectID != capsule.ProjectID ||
		attempt.Executor.Model == "" || capsule.Budgets.WallTimeSeconds < 1 {
		return ModelCapability{}, ErrModelCapability
	}
	now := authority.now().UTC().Truncate(time.Microsecond)
	expiresAt := now.Add(time.Duration(capsule.Budgets.WallTimeSeconds)*time.Second + 2*time.Minute)
	record := ModelCapabilityRecord{
		SchemaVersion: modelCapabilitySchemaVersion,
		ID:            uuid.NewString(), AttemptID: attempt.ID, ProjectID: attempt.ProjectID,
		Model: attempt.Executor.Model, MaxInputTokens: capsule.Budgets.MaxInputTokens,
		MaxOutputTokens: capsule.Budgets.MaxOutputTokens, ExpiresAt: expiresAt,
	}
	if err := validateModelCapabilityRecord(record, now); err != nil {
		return ModelCapability{}, err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return ModelCapability{}, err
	}
	for collision := 0; collision < 3; collision++ {
		token, tokenHash, err := newModelCapabilityToken()
		if err != nil {
			return ModelCapability{}, err
		}
		created, err := authority.create(ctx, record.ID, tokenHash, payload, expiresAt.Sub(now))
		if err != nil {
			return ModelCapability{}, fmt.Errorf("%w: store short-lived capability: %v", ErrModelCapability, err)
		}
		if created {
			return ModelCapability{ID: record.ID, Token: token, BaseURL: authority.baseURL, ExpiresAt: expiresAt}, nil
		}
	}
	return ModelCapability{}, fmt.Errorf("%w: token collision", ErrModelCapability)
}

func (authority *RedisModelCapabilityAuthority) Revoke(ctx context.Context, id string) error {
	if authority == nil || ctx == nil || !validUUIDs(id) {
		return ErrModelCapability
	}
	script := redis.NewScript(`
local token_hash = redis.call('GET', KEYS[1])
if not token_hash then
  return 0
end
redis.call('DEL', KEYS[1])
redis.call('DEL', ARGV[1] .. token_hash)
redis.call('DEL', ARGV[2])
	redis.call('DEL', ARGV[3])
	redis.call('DEL', ARGV[4])
return 1
`)
	if _, err := script.Run(
		ctx,
		authority.client,
		[]string{authority.idKey(id)},
		authority.prefix+"cap:",
		authority.prefix+"use:"+id,
		authority.prefix+"input:"+id,
		authority.prefix+"output:"+id,
	).Result(); err != nil {
		return fmt.Errorf("%w: revoke capability: %v", ErrModelCapability, err)
	}
	return nil
}

func (authority *RedisModelCapabilityAuthority) Authenticate(
	ctx context.Context,
	token string,
) (ModelCapabilityRecord, error) {
	if authority == nil || ctx == nil || token != strings.TrimSpace(token) || len(token) < 32 || len(token) > 200 {
		return ModelCapabilityRecord{}, ErrModelCapability
	}
	tokenHash := modelCapabilityTokenHash(token)
	payload, err := authority.client.Get(ctx, authority.capabilityKey(tokenHash)).Bytes()
	if errors.Is(err, redis.Nil) {
		return ModelCapabilityRecord{}, ErrModelCapability
	}
	if err != nil {
		return ModelCapabilityRecord{}, fmt.Errorf("%w: read capability: %v", ErrModelCapability, err)
	}
	var record ModelCapabilityRecord
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return ModelCapabilityRecord{}, fmt.Errorf("%w: decode capability", ErrModelCapability)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ModelCapabilityRecord{}, fmt.Errorf("%w: trailing capability data", ErrModelCapability)
	}
	if err := validateModelCapabilityRecord(record, authority.now().UTC()); err != nil {
		return ModelCapabilityRecord{}, err
	}
	return record, nil
}

func (authority *RedisModelCapabilityAuthority) Acquire(
	ctx context.Context,
	record ModelCapabilityRecord,
	ttl time.Duration,
	inputTokenUpperBound, requestedOutputTokens int64,
) (ModelBudgetLease, error) {
	if authority == nil || ctx == nil || validateModelCapabilityRecord(record, authority.now().UTC()) != nil ||
		ttl <= 0 || ttl > 8*time.Hour+2*time.Minute || inputTokenUpperBound < 1 ||
		requestedOutputTokens < 1 || requestedOutputTokens > record.MaxOutputTokens {
		return ModelBudgetLease{}, ErrModelCapability
	}
	now := authority.now().UTC()
	remaining := record.ExpiresAt.Sub(now)
	if ttl > remaining {
		ttl = remaining
	}
	if ttl < time.Millisecond || remaining < time.Millisecond {
		return ModelBudgetLease{}, ErrModelCapability
	}
	leaseID := uuid.NewString()
	script := redis.NewScript(`
if redis.call('EXISTS', KEYS[4]) ~= 1 then
  return -3
end
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 0
end
local input_used = tonumber(redis.call('GET', KEYS[2]) or '0')
local output_used = tonumber(redis.call('GET', KEYS[3]) or '0')
local input_request = tonumber(ARGV[1])
local output_request = tonumber(ARGV[2])
local max_input = tonumber(ARGV[3])
local max_output = tonumber(ARGV[4])
if input_used + input_request > max_input then
  return -1
end
local output_remaining = max_output - output_used
if output_remaining < 1 then
  return -2
end
local output_allowance = output_request
if output_allowance > output_remaining then
  output_allowance = output_remaining
end
redis.call('PSETEX', KEYS[1], ARGV[5], ARGV[7])
redis.call('PSETEX', KEYS[2], ARGV[6], input_used + input_request)
redis.call('PSETEX', KEYS[3], ARGV[6], output_used + output_allowance)
return output_allowance
`)
	result, err := script.Run(
		ctx,
		authority.client,
		[]string{
			authority.useKey(record), authority.inputUseKey(record), authority.outputUseKey(record),
			authority.idKey(record.ID),
		},
		inputTokenUpperBound,
		requestedOutputTokens,
		record.MaxInputTokens,
		record.MaxOutputTokens,
		ttl.Milliseconds(),
		remaining.Milliseconds(),
		leaseID,
	).Int64()
	if err != nil {
		return ModelBudgetLease{}, fmt.Errorf("%w: acquire capability use: %v", ErrModelCapability, err)
	}
	switch result {
	case 0:
		return ModelBudgetLease{}, ErrModelCapabilityBusy
	case -1:
		return ModelBudgetLease{}, ErrModelInputBudgetExceeded
	case -2:
		return ModelBudgetLease{}, ErrModelOutputBudgetExceeded
	case -3:
		return ModelBudgetLease{}, ErrModelCapability
	}
	if result < 1 || result > requestedOutputTokens {
		return ModelBudgetLease{}, fmt.Errorf("%w: invalid model budget lease", ErrModelCapability)
	}
	return ModelBudgetLease{
		ID: leaseID, InputTokenUpperBound: inputTokenUpperBound, OutputTokenAllowance: result,
	}, nil
}

func (authority *RedisModelCapabilityAuthority) Release(
	ctx context.Context,
	record ModelCapabilityRecord,
	lease ModelBudgetLease,
	observedInputTokens int64,
	observedOutputTokens int64,
	usageAvailable bool,
) error {
	if authority == nil || ctx == nil || !validUUIDs(record.ID, lease.ID) ||
		lease.InputTokenUpperBound < 1 || lease.OutputTokenAllowance < 1 ||
		observedInputTokens < 0 || observedInputTokens > lease.InputTokenUpperBound ||
		observedOutputTokens < 0 || observedOutputTokens > lease.OutputTokenAllowance {
		return ErrModelCapability
	}
	inputRefund, outputRefund := int64(0), int64(0)
	if usageAvailable {
		inputRefund = lease.InputTokenUpperBound - observedInputTokens
		outputRefund = lease.OutputTokenAllowance - observedOutputTokens
	}
	script := redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
local input_used = tonumber(redis.call('GET', KEYS[2]) or '0')
local output_used = tonumber(redis.call('GET', KEYS[3]) or '0')
local input_refund = tonumber(ARGV[2])
local output_refund = tonumber(ARGV[3])
if input_refund < 0 or output_refund < 0 or input_used < input_refund or output_used < output_refund then
  return -1
end
if input_refund > 0 then
  local ttl = redis.call('PTTL', KEYS[2])
  if ttl > 0 then
    redis.call('PSETEX', KEYS[2], ttl, input_used - input_refund)
  end
end
if output_refund > 0 then
  local ttl = redis.call('PTTL', KEYS[3])
  if ttl > 0 then
    redis.call('PSETEX', KEYS[3], ttl, output_used - output_refund)
  end
end
redis.call('DEL', KEYS[1])
return 1
`)
	result, err := script.Run(
		ctx,
		authority.client,
		[]string{authority.useKey(record), authority.inputUseKey(record), authority.outputUseKey(record)},
		lease.ID,
		inputRefund,
		outputRefund,
	).Int()
	if err != nil {
		return fmt.Errorf("%w: release capability use: %v", ErrModelCapability, err)
	}
	if result != 1 {
		return fmt.Errorf("%w: stale model budget lease", ErrModelCapability)
	}
	return nil
}

func (authority *RedisModelCapabilityAuthority) create(
	ctx context.Context,
	id, tokenHash string,
	payload []byte,
	ttl time.Duration,
) (bool, error) {
	if ttl <= 0 {
		return false, ErrModelCapability
	}
	script := redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 or redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
redis.call('PSETEX', KEYS[1], ARGV[1], ARGV[2])
redis.call('PSETEX', KEYS[2], ARGV[1], ARGV[3])
return 1
`)
	result, err := script.Run(
		ctx,
		authority.client,
		[]string{authority.capabilityKey(tokenHash), authority.idKey(id)},
		ttl.Milliseconds(),
		payload,
		tokenHash,
	).Int()
	return result == 1, err
}

func (authority *RedisModelCapabilityAuthority) capabilityKey(tokenHash string) string {
	return authority.prefix + "cap:" + tokenHash
}

func (authority *RedisModelCapabilityAuthority) idKey(id string) string {
	return authority.prefix + "id:" + id
}

func (authority *RedisModelCapabilityAuthority) useKey(record ModelCapabilityRecord) string {
	return authority.prefix + "use:" + record.ID
}

func (authority *RedisModelCapabilityAuthority) inputUseKey(record ModelCapabilityRecord) string {
	return authority.prefix + "input:" + record.ID
}

func (authority *RedisModelCapabilityAuthority) outputUseKey(record ModelCapabilityRecord) string {
	return authority.prefix + "output:" + record.ID
}

type ModelGatewayConfig struct {
	UpstreamResponsesURL string
	UpstreamAPIKey       string
	Organization         string
	UpstreamProject      string
	MaxInputBytes        int64
	MaxOutputBytes       int64
	RequestTimeout       time.Duration
}

type ModelGateway struct {
	config       ModelGatewayConfig
	capabilities ModelGatewayCapabilities
	client       *http.Client
	logger       *slog.Logger
}

func NewModelGateway(
	config ModelGatewayConfig,
	capabilities ModelGatewayCapabilities,
	client *http.Client,
	logger *slog.Logger,
) (*ModelGateway, error) {
	config.UpstreamResponsesURL = strings.TrimSpace(config.UpstreamResponsesURL)
	config.UpstreamAPIKey = strings.TrimSpace(config.UpstreamAPIKey)
	config.Organization = strings.TrimSpace(config.Organization)
	config.UpstreamProject = strings.TrimSpace(config.UpstreamProject)
	parsed, err := url.Parse(config.UpstreamResponsesURL)
	if capabilities == nil || config.UpstreamAPIKey == "" ||
		strings.ContainsAny(config.UpstreamAPIKey+config.Organization+config.UpstreamProject, "\r\n\x00") || err != nil ||
		parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasSuffix(parsed.Path, "/responses") ||
		config.MaxInputBytes < 1024 || config.MaxInputBytes > 16<<20 ||
		config.MaxOutputBytes < 1024 || config.MaxOutputBytes > 64<<20 ||
		config.RequestTimeout < time.Second || config.RequestTimeout > 8*time.Hour+time.Minute {
		return nil, fmt.Errorf("%w: Model Gateway configuration", ErrModelGateway)
	}
	if client == nil {
		client = &http.Client{Timeout: config.RequestTimeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ModelGateway{config: config, capabilities: capabilities, client: client, logger: logger}, nil
}

func (gateway *ModelGateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	if gateway == nil || request == nil || request.Method != http.MethodPost {
		writeModelGatewayError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	token, ok := bearerCapability(request.Header.Get("Authorization"))
	if !ok {
		writeModelGatewayError(writer, http.StatusUnauthorized, "invalid_model_capability")
		return
	}
	record, err := gateway.capabilities.Authenticate(request.Context(), token)
	if err != nil {
		writeModelGatewayError(writer, http.StatusUnauthorized, "invalid_model_capability")
		return
	}

	body, err := io.ReadAll(io.LimitReader(request.Body, gateway.config.MaxInputBytes+1))
	if err != nil || int64(len(body)) > gateway.config.MaxInputBytes {
		writeModelGatewayError(writer, http.StatusRequestEntityTooLarge, "model_request_too_large")
		return
	}
	body, err = qualifyModelGatewayRequest(body, record)
	if err != nil {
		writeModelGatewayError(writer, http.StatusBadRequest, "model_request_policy_denied")
		return
	}
	requestedOutputTokens, err := modelRequestMaxOutputTokens(body)
	if err != nil {
		writeModelGatewayError(writer, http.StatusBadRequest, "model_request_policy_denied")
		return
	}
	inputTokenUpperBound, err := conservativeInputTokenUpperBound(body)
	if err != nil {
		writeModelGatewayError(writer, http.StatusBadRequest, "model_request_policy_denied")
		return
	}
	lease, err := gateway.capabilities.Acquire(
		request.Context(), record, gateway.config.RequestTimeout,
		inputTokenUpperBound, requestedOutputTokens,
	)
	if err != nil {
		switch {
		case errors.Is(err, ErrModelCapabilityBusy):
			writeModelGatewayError(writer, http.StatusTooManyRequests, "model_capability_busy")
		case errors.Is(err, ErrModelInputBudgetExceeded):
			writeModelGatewayError(writer, http.StatusTooManyRequests, "model_input_budget_exceeded")
		case errors.Is(err, ErrModelOutputBudgetExceeded):
			writeModelGatewayError(writer, http.StatusTooManyRequests, "model_output_budget_exhausted")
		default:
			writeModelGatewayError(writer, http.StatusTooManyRequests, "model_capability_unavailable")
		}
		return
	}
	observedInputTokens, observedOutputTokens := int64(0), int64(0)
	usageAvailable := false
	defer func() {
		if releaseErr := gateway.capabilities.Release(
			context.WithoutCancel(request.Context()), record, lease,
			observedInputTokens, observedOutputTokens, usageAvailable,
		); releaseErr != nil {
			gateway.logger.Error(
				"Agent Model Gateway budget lease release failed",
				"capability_id", record.ID, "attempt_id", record.AttemptID, "error", releaseErr,
			)
		}
	}()
	body, err = qualifyModelGatewayRequestWithLimit(body, record, lease.OutputTokenAllowance)
	if err != nil {
		writeModelGatewayError(writer, http.StatusBadRequest, "model_request_policy_denied")
		return
	}
	upstream, err := http.NewRequestWithContext(
		request.Context(), http.MethodPost, gateway.config.UpstreamResponsesURL, bytes.NewReader(body),
	)
	if err != nil {
		writeModelGatewayError(writer, http.StatusBadGateway, "model_upstream_unavailable")
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+gateway.config.UpstreamAPIKey)
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", request.Header.Get("Accept"))
	if upstream.Header.Get("Accept") == "" {
		upstream.Header.Set("Accept", "application/json, text/event-stream")
	}
	if beta := strings.TrimSpace(request.Header.Get("OpenAI-Beta")); beta != "" && len(beta) <= 500 {
		upstream.Header.Set("OpenAI-Beta", beta)
	}
	if gateway.config.Organization != "" {
		upstream.Header.Set("OpenAI-Organization", gateway.config.Organization)
	}
	if gateway.config.UpstreamProject != "" {
		upstream.Header.Set("OpenAI-Project", gateway.config.UpstreamProject)
	}
	response, err := gateway.client.Do(upstream)
	if err != nil {
		writeModelGatewayError(writer, http.StatusBadGateway, "model_upstream_unavailable")
		gateway.audit(record, lease, 0, 0, false, 0, 0, started, err)
		return
	}
	defer response.Body.Close()
	for _, header := range []string{"Content-Type", "OpenAI-Request-ID", "X-Request-ID", "Retry-After"} {
		if value := response.Header.Get(header); value != "" {
			writer.Header().Set(header, value)
		}
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(response.StatusCode)
	observer := newModelUsageObserver(response.Header.Get("Content-Type"))
	written, copyErr := copyBoundedStreaming(
		&modelUsageObservingWriter{writer: writer, observer: observer},
		response.Body,
		gateway.config.MaxOutputBytes,
	)
	if inputUsage, outputUsage, ok := observer.Usage(); ok &&
		inputUsage >= 0 && inputUsage <= lease.InputTokenUpperBound &&
		outputUsage >= 0 && outputUsage <= lease.OutputTokenAllowance {
		observedInputTokens, observedOutputTokens, usageAvailable = inputUsage, outputUsage, true
	} else if ok && copyErr == nil {
		copyErr = fmt.Errorf("%w: upstream usage exceeded the capability allowance", ErrModelGateway)
	}
	gateway.audit(
		record, lease, observedInputTokens, observedOutputTokens, usageAvailable,
		response.StatusCode, written, started, copyErr,
	)
}

func qualifyModelGatewayRequest(body []byte, record ModelCapabilityRecord) ([]byte, error) {
	return qualifyModelGatewayRequestWithLimit(body, record, record.MaxOutputTokens)
}

func qualifyModelGatewayRequestWithLimit(
	body []byte,
	record ModelCapabilityRecord,
	maximumOutputTokens int64,
) ([]byte, error) {
	if maximumOutputTokens < 1 || maximumOutputTokens > record.MaxOutputTokens {
		return nil, ErrModelGateway
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil || len(request) == 0 {
		return nil, ErrModelGateway
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrModelGateway
	}
	model, ok := request["model"].(string)
	if !ok || model != record.Model {
		return nil, ErrModelGateway
	}
	for _, forbidden := range []string{"conversation", "previous_response_id"} {
		if value, exists := request[forbidden]; exists && value != nil && value != "" {
			return nil, ErrModelGateway
		}
	}
	if background, exists := request["background"]; exists && background != false && background != nil {
		return nil, ErrModelGateway
	}
	if store, exists := request["store"]; exists && store != false && store != nil {
		return nil, ErrModelGateway
	}
	request["store"] = false
	if value, exists := request["max_output_tokens"]; exists {
		number, ok := value.(json.Number)
		if !ok {
			return nil, ErrModelGateway
		}
		maximum, err := number.Int64()
		if err != nil || maximum < 1 || maximum > record.MaxOutputTokens {
			return nil, ErrModelGateway
		}
		if maximum > maximumOutputTokens {
			request["max_output_tokens"] = maximumOutputTokens
		}
	} else {
		request["max_output_tokens"] = maximumOutputTokens
	}
	return json.Marshal(request)
}

func modelRequestMaxOutputTokens(body []byte) (int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return 0, ErrModelGateway
	}
	value, ok := request["max_output_tokens"].(json.Number)
	if !ok {
		return 0, ErrModelGateway
	}
	maximum, err := value.Int64()
	if err != nil || maximum < 1 {
		return 0, ErrModelGateway
	}
	return maximum, nil
}

// conservativeInputTokenUpperBound is intentionally not a tokenizer. It
// reserves one admission unit for every normalized UTF-8 request byte, a fixed
// Responses protocol envelope, and additional units for every JSON node. This
// deliberately over-reserves ordinary requests and must be re-qualified for
// every supported model/provider protocol; observed provider usage remains the
// authoritative post-execution token measurement.
func conservativeInputTokenUpperBound(body []byte) (int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, ErrModelGateway
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return 0, ErrModelGateway
	}
	nodes, err := countModelRequestJSONNodes(value, 0)
	if err != nil {
		return 0, err
	}
	const protocolEnvelope = int64(4096)
	const perNodeEnvelope = int64(64)
	if int64(len(body)) > int64(^uint64(0)>>1)-protocolEnvelope-nodes*perNodeEnvelope {
		return 0, ErrModelGateway
	}
	return int64(len(body)) + protocolEnvelope + nodes*perNodeEnvelope, nil
}

func countModelRequestJSONNodes(value any, depth int) (int64, error) {
	if depth > 128 {
		return 0, ErrModelGateway
	}
	nodes := int64(1)
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			count, err := countModelRequestJSONNodes(item, depth+1)
			if err != nil || nodes > 100_000-count {
				return 0, ErrModelGateway
			}
			nodes += count
		}
	case map[string]any:
		for _, item := range typed {
			count, err := countModelRequestJSONNodes(item, depth+1)
			if err != nil || nodes > 100_000-count {
				return 0, ErrModelGateway
			}
			nodes += count
		}
	}
	return nodes, nil
}

const maximumModelUsageObservationBytes = 4 << 20

type modelUsageObserver struct {
	contentType string
	buffer      bytes.Buffer
	overflow    bool
}

type modelUsageObservingWriter struct {
	writer   io.Writer
	observer *modelUsageObserver
}

func newModelUsageObserver(contentType string) *modelUsageObserver {
	return &modelUsageObserver{contentType: strings.ToLower(strings.TrimSpace(contentType))}
}

func (writer *modelUsageObservingWriter) Write(value []byte) (int, error) {
	written, err := writer.writer.Write(value)
	if written > 0 && writer.observer != nil {
		writer.observer.Observe(value[:written])
	}
	return written, err
}

func (writer *modelUsageObservingWriter) Flush() {
	if flusher, ok := writer.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (observer *modelUsageObserver) Observe(value []byte) {
	if observer == nil || observer.overflow || len(value) == 0 {
		return
	}
	if observer.buffer.Len()+len(value) > maximumModelUsageObservationBytes {
		observer.buffer.Reset()
		observer.overflow = true
		return
	}
	_, _ = observer.buffer.Write(value)
}

func (observer *modelUsageObserver) Usage() (int64, int64, bool) {
	if observer == nil || observer.overflow || observer.buffer.Len() == 0 {
		return 0, 0, false
	}
	payload := observer.buffer.Bytes()
	if strings.Contains(observer.contentType, "text/event-stream") {
		inputUsage, outputUsage, available := int64(0), int64(0), false
		for _, rawLine := range bytes.Split(payload, []byte{'\n'}) {
			line := bytes.TrimSpace(rawLine)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if bytes.Equal(data, []byte("[DONE]")) {
				continue
			}
			if observedInput, observedOutput, ok := responseTokenUsage(data); ok {
				inputUsage, outputUsage, available = observedInput, observedOutput, true
			}
		}
		return inputUsage, outputUsage, available
	}
	return responseTokenUsage(payload)
}

func responseTokenUsage(payload []byte) (int64, int64, bool) {
	var event struct {
		Type  string `json:"type"`
		Usage *struct {
			InputTokens  *int64 `json:"input_tokens"`
			OutputTokens *int64 `json:"output_tokens"`
		} `json:"usage,omitempty"`
		Response *struct {
			Usage *struct {
				InputTokens  *int64 `json:"input_tokens"`
				OutputTokens *int64 `json:"output_tokens"`
			} `json:"usage,omitempty"`
		} `json:"response,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&event); err != nil {
		return 0, 0, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return 0, 0, false
	}
	if event.Response != nil && event.Response.Usage != nil && event.Response.Usage.InputTokens != nil &&
		event.Response.Usage.OutputTokens != nil &&
		(event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed") {
		return *event.Response.Usage.InputTokens, *event.Response.Usage.OutputTokens,
			*event.Response.Usage.InputTokens >= 0 && *event.Response.Usage.OutputTokens >= 0
	}
	if event.Type == "" && event.Usage != nil && event.Usage.InputTokens != nil && event.Usage.OutputTokens != nil {
		return *event.Usage.InputTokens, *event.Usage.OutputTokens,
			*event.Usage.InputTokens >= 0 && *event.Usage.OutputTokens >= 0
	}
	return 0, 0, false
}

func copyBoundedStreaming(writer io.Writer, reader io.Reader, maximum int64) (int64, error) {
	buffer := make([]byte, 32<<10)
	written := int64(0)
	for {
		count, readErr := reader.Read(buffer)
		if count > 0 {
			if written+int64(count) > maximum {
				allowed := maximum - written
				if allowed > 0 {
					copied, writeErr := writer.Write(buffer[:allowed])
					written += int64(copied)
					if writeErr != nil {
						return written, writeErr
					}
				}
				return written, fmt.Errorf("%w: upstream response exceeded the bound", ErrModelGateway)
			}
			copied, writeErr := writer.Write(buffer[:count])
			written += int64(copied)
			if writeErr != nil {
				return written, writeErr
			}
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func (gateway *ModelGateway) audit(
	record ModelCapabilityRecord,
	lease ModelBudgetLease,
	observedInputTokens int64,
	observedOutputTokens int64,
	usageAvailable bool,
	status int,
	responseBytes int64,
	started time.Time,
	err error,
) {
	level := slog.LevelInfo
	if err != nil || status >= 500 {
		level = slog.LevelError
	}
	gateway.logger.Log(
		context.Background(),
		level,
		"Agent Model Gateway request",
		"capability_id", record.ID,
		"attempt_id", record.AttemptID,
		"project_id", record.ProjectID,
		"model", record.Model,
		"input_token_admission_upper_bound", lease.InputTokenUpperBound,
		"observed_input_tokens", observedInputTokens,
		"output_token_allowance", lease.OutputTokenAllowance,
		"observed_output_tokens", observedOutputTokens,
		"output_usage_available", usageAvailable,
		"status", status,
		"response_bytes", responseBytes,
		"duration_ms", time.Since(started).Milliseconds(),
		"error", err,
	)
}

func validateModelCapabilityRecord(record ModelCapabilityRecord, now time.Time) error {
	model := strings.TrimSpace(record.Model)
	if record.SchemaVersion != modelCapabilitySchemaVersion ||
		!validUUIDs(record.ID, record.AttemptID, record.ProjectID) || model == "" || model != record.Model ||
		len(model) > 160 || strings.ContainsAny(model, "\r\n\x00") ||
		record.MaxInputTokens < 1 || record.MaxInputTokens > 4_000_000 ||
		record.MaxOutputTokens < 1 || record.MaxOutputTokens > 1_000_000 ||
		record.ExpiresAt.IsZero() || !record.ExpiresAt.After(now) ||
		record.ExpiresAt.After(now.Add(8*time.Hour+3*time.Minute)) {
		return ErrModelCapability
	}
	return nil
}

func newModelCapabilityToken() (string, string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", "", fmt.Errorf("%w: generate token", ErrModelCapability)
	}
	token := base64.RawURLEncoding.EncodeToString(value)
	return token, modelCapabilityTokenHash(token), nil
}

func modelCapabilityTokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func bearerCapability(value string) (string, bool) {
	parts := strings.Split(value, " ")
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" || parts[1] != strings.TrimSpace(parts[1]) {
		return "", false
	}
	return parts[1], true
}

func writeModelGatewayError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]string{
			"type":    "agent_model_gateway_error",
			"code":    code,
			"message": "The attempt-scoped model request was rejected.",
		},
	})
}

var (
	_ ModelCapabilityIssuer    = (*RedisModelCapabilityAuthority)(nil)
	_ ModelGatewayCapabilities = (*RedisModelCapabilityAuthority)(nil)
	_ http.Handler             = (*ModelGateway)(nil)
)
