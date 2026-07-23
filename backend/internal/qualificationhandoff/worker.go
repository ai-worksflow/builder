package qualificationhandoff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	PendingHandoffSubject   = "worksflow.qualification.promotion-handoff.pending"
	PendingHandoffEventType = "qualification.promotion_handoff.pending"
	PendingHandoffDurable   = "worksflow-qualification-handoff-v1"

	HandoffQuarantineSubject   = "worksflow.qualification.promotion-handoff.quarantine"
	HandoffQuarantineEventType = "qualification.promotion_handoff.quarantined"
	HandoffQuarantineSchemaV1  = "worksflow-qualification-handoff-quarantine/v1"

	maximumPendingHandoffPayloadBytes = 256
	maximumTerminalPayloadBytes       = 256
	maximumTerminalMessageIDBytes     = 128
	maximumTerminalSubjectBytes       = 256
	maximumTerminalEventTypeBytes     = 128
	maximumTerminalEventTypeCount     = 8
	maximumTerminalDetailBytes        = 512
	maximumBrokerHeaderBytes          = 4 << 10

	maximumWorkerInFlight           = 256
	maximumTerminalAttemptThreshold = 1_000_000
	maximumWorkerOperationTimeout   = 30 * time.Minute
	maximumWorkerTerminalTimeout    = 5 * time.Minute
	maximumWorkerRetryBackOff       = time.Hour
	maximumWorkerRetryBackOffCount  = 64
	maximumConsumerAckConfirmWait   = time.Minute
	maximumConsumerAckWait          = 2 * time.Hour
	maximumConsumerAckPending       = 4096
	maximumConsumerRequestBatch     = 256
	maximumConsumerRequestMaxBytes  = 64 << 20
)

// CompletionService is intentionally narrower than AtomicStore. A delivery
// supplies only an opaque handoff ID and cannot supply project, Revision,
// Promotion, or workflow facts to either database operation.
type CompletionService interface {
	Complete(context.Context, uuid.UUID) (Record, error)
	Inspect(context.Context, uuid.UUID) (Record, error)
}

// Delivery is the broker-independent manual-ack boundary used by Worker.
// Attempt is one-based. Payload is read-only for the duration of Handle.
// EventTypes preserves all header values so a message
// cannot smuggle an ambiguous duplicate Worksflow-Event-Type header through a
// first-value-only API.
type Delivery interface {
	Subject() string
	EventTypes() []string
	Payload() []byte
	MessageID() string
	MessageIDs() []string
	StreamSequence() uint64
	Attempt() uint64
	Ack(context.Context) error
	Nak(context.Context, time.Duration) error
	Term(context.Context) error
}

type ConsumerLimits struct {
	MaxDeliver         int
	MaxAckPending      int
	AckWait            time.Duration
	AckConfirmWait     time.Duration
	MaxRequestBatch    int
	MaxRequestMaxBytes int
}

// DeliveryConsumer must be backed by one explicit durable consumer. Fetch is
// allowed to return an empty batch on a bounded poll timeout.
type DeliveryConsumer interface {
	Fetch(context.Context, int) ([]Delivery, error)
	Limits() ConsumerLimits
	Close() error
}

// QuarantineSink durably records a terminal delivery before Worker asks the
// source consumer to TERM it. If this write fails, the source is left
// unacknowledged (and normally NAKed), so invalid or corrupt messages are not
// silently discarded.
type QuarantineSink interface {
	Quarantine(context.Context, QuarantineRecord) error
}

type QuarantineRecord struct {
	SchemaVersion             string   `json:"schemaVersion"`
	SourceMessageID           string   `json:"sourceMessageId"`
	SourceMessageIDTruncated  bool     `json:"sourceMessageIdTruncated"`
	SourceStreamSequence      uint64   `json:"sourceStreamSequence"`
	SourceSubject             string   `json:"sourceSubject"`
	SourceSubjectTruncated    bool     `json:"sourceSubjectTruncated"`
	SourceEventTypes          []string `json:"sourceEventTypes"`
	SourceEventTypesTruncated bool     `json:"sourceEventTypesTruncated"`
	SourcePayloadBase64       string   `json:"sourcePayloadBase64"`
	SourcePayloadHash         string   `json:"sourcePayloadHash"`
	SourcePayloadSize         int      `json:"sourcePayloadSize"`
	SourcePayloadTruncated    bool     `json:"sourcePayloadTruncated"`
	DeliveryAttempt           uint64   `json:"deliveryAttempt"`
	HandoffID                 *string  `json:"handoffId"`
	Reason                    string   `json:"reason"`
	Detail                    string   `json:"detail"`
	QuarantinedAt             string   `json:"quarantinedAt"`
}

type WorkerConfig struct {
	MaxInFlight              int
	TerminalAttemptThreshold int
	OperationTimeout         time.Duration
	TerminalTimeout          time.Duration
	RetryBackOff             []time.Duration
}

func DefaultWorkerConfig() WorkerConfig {
	return WorkerConfig{
		MaxInFlight:              8,
		TerminalAttemptThreshold: 20,
		OperationTimeout:         45 * time.Second,
		TerminalTimeout:          5 * time.Second,
		RetryBackOff: []time.Duration{
			time.Second,
			5 * time.Second,
			15 * time.Second,
			30 * time.Second,
			60 * time.Second,
		},
	}
}

type Worker struct {
	service    CompletionService
	consumer   DeliveryConsumer
	quarantine QuarantineSink
	config     WorkerConfig
	now        func() time.Time
}

func NewWorker(
	service CompletionService,
	consumer DeliveryConsumer,
	quarantine QuarantineSink,
	config WorkerConfig,
) (*Worker, error) {
	if isNilInterface(service) || isNilInterface(consumer) || isNilInterface(quarantine) {
		return nil, errors.New("qualification handoff service, durable consumer, and quarantine sink are required")
	}
	if err := validateWorkerConfig(config); err != nil {
		return nil, err
	}
	limits := consumer.Limits()
	if limits.MaxDeliver != -1 {
		return nil, fmt.Errorf("qualification handoff consumer MaxDeliver=%d must be JetStream unlimited (-1)", limits.MaxDeliver)
	}
	if limits.MaxAckPending <= 0 || limits.MaxAckPending > maximumConsumerAckPending ||
		config.MaxInFlight > limits.MaxAckPending {
		return nil, fmt.Errorf("qualification handoff MaxInFlight=%d exceeds consumer MaxAckPending=%d", config.MaxInFlight, limits.MaxAckPending)
	}
	if limits.MaxRequestBatch <= 0 || limits.MaxRequestBatch > maximumConsumerRequestBatch ||
		config.MaxInFlight > limits.MaxRequestBatch {
		return nil, fmt.Errorf("qualification handoff MaxInFlight=%d exceeds consumer MaxRequestBatch=%d", config.MaxInFlight, limits.MaxRequestBatch)
	}
	minimumRequestBytes, ok := safeMultiplyInt(config.MaxInFlight, maximumPendingHandoffPayloadBytes+maximumBrokerHeaderBytes)
	if !ok || limits.MaxRequestMaxBytes < minimumRequestBytes || limits.MaxRequestMaxBytes > maximumConsumerRequestMaxBytes {
		return nil, fmt.Errorf("qualification handoff consumer MaxRequestMaxBytes=%d cannot carry the bounded in-flight batch of %d bytes", limits.MaxRequestMaxBytes, minimumRequestBytes)
	}
	if limits.AckConfirmWait <= 0 || limits.AckConfirmWait > maximumConsumerAckConfirmWait {
		return nil, fmt.Errorf("qualification handoff consumer AckConfirmWait=%s is invalid", limits.AckConfirmWait)
	}
	minimumAckWait, err := requiredConsumerAckWait(config, limits.AckConfirmWait)
	if err != nil {
		return nil, err
	}
	if limits.AckWait < minimumAckWait || limits.AckWait > maximumConsumerAckWait {
		return nil, fmt.Errorf("qualification handoff consumer AckWait=%s is shorter than the bounded complete/inspect window %s", limits.AckWait, minimumAckWait)
	}
	return &Worker{
		service: service, consumer: consumer, quarantine: quarantine,
		config: cloneWorkerConfig(config), now: time.Now,
	}, nil
}

func validateWorkerConfig(config WorkerConfig) error {
	if config.MaxInFlight <= 0 || config.MaxInFlight > maximumWorkerInFlight ||
		config.TerminalAttemptThreshold < 1 || config.TerminalAttemptThreshold > maximumTerminalAttemptThreshold ||
		config.OperationTimeout <= 0 || config.OperationTimeout > maximumWorkerOperationTimeout ||
		config.TerminalTimeout <= 0 || len(config.RetryBackOff) == 0 {
		return errors.New("qualification handoff worker limits, terminal threshold, and timeouts are invalid")
	}
	if config.TerminalTimeout > maximumWorkerTerminalTimeout || len(config.RetryBackOff) > maximumWorkerRetryBackOffCount {
		return errors.New("qualification handoff worker terminal timeout or retry schedule exceeds its bound")
	}
	for _, delay := range config.RetryBackOff {
		if delay <= 0 || delay > maximumWorkerRetryBackOff {
			return errors.New("qualification handoff worker retry backoff values must be positive")
		}
	}
	return nil
}

func requiredConsumerAckWait(config WorkerConfig, ackConfirmWait time.Duration) (time.Duration, error) {
	values := []time.Duration{
		config.OperationTimeout,
		commitUnknownInspectTimeout,
		config.OperationTimeout,
		config.TerminalTimeout,
		ackConfirmWait,
	}
	total := time.Duration(0)
	for _, value := range values {
		if value <= 0 || total > time.Duration(1<<63-1)-value {
			return 0, errors.New("qualification handoff AckWait calculation overflowed")
		}
		total += value
	}
	if total > maximumConsumerAckWait {
		return 0, fmt.Errorf("qualification handoff required AckWait=%s exceeds the supported bound", total)
	}
	return total, nil
}

func safeMultiplyInt(left, right int) (int, bool) {
	if left <= 0 || right <= 0 || left > int(^uint(0)>>1)/right {
		return 0, false
	}
	return left * right, true
}

func cloneWorkerConfig(config WorkerConfig) WorkerConfig {
	cloned := config
	cloned.RetryBackOff = append([]time.Duration(nil), config.RetryBackOff...)
	return cloned
}

func (worker *Worker) Run(ctx context.Context) error {
	if worker == nil || isNilInterface(worker.consumer) || isNilInterface(ctx) {
		return errors.New("qualification handoff worker and context are required")
	}
	defer worker.consumer.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := worker.RunOnce(ctx); err != nil {
			return err
		}
	}
}

func (worker *Worker) RunOnce(ctx context.Context) (int, error) {
	if worker == nil || isNilInterface(worker.consumer) || isNilInterface(ctx) {
		return 0, errors.New("qualification handoff worker and context are required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	deliveries, err := worker.consumer.Fetch(ctx, worker.config.MaxInFlight)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("fetch qualification handoff deliveries: %w", err)
	}
	if len(deliveries) > worker.config.MaxInFlight {
		return 0, errors.New("qualification handoff consumer exceeded the bounded in-flight batch")
	}

	results := make(chan error, len(deliveries))
	var wait sync.WaitGroup
	for _, delivery := range deliveries {
		if isNilInterface(delivery) {
			results <- errors.New("qualification handoff consumer returned a nil delivery")
			continue
		}
		wait.Add(1)
		go func(delivery Delivery) {
			defer wait.Done()
			results <- worker.Handle(ctx, delivery)
		}(delivery)
	}
	wait.Wait()
	close(results)
	var batchErrors []error
	for result := range results {
		if result != nil {
			batchErrors = append(batchErrors, result)
		}
	}
	return len(deliveries), errors.Join(batchErrors...)
}

func (worker *Worker) Handle(ctx context.Context, delivery Delivery) error {
	if worker == nil || isNilInterface(worker.service) || isNilInterface(worker.quarantine) ||
		isNilInterface(ctx) || isNilInterface(delivery) {
		return errors.New("qualification handoff worker, delivery, and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	handoffID, decodeErr := decodePendingHandoff(delivery)
	if decodeErr != nil {
		return worker.terminal(ctx, delivery, uuid.Nil, "invalid_message", decodeErr)
	}

	operationCtx, cancel := context.WithTimeout(ctx, worker.config.OperationTimeout)
	record, completeErr := worker.service.Complete(operationCtx, handoffID)
	cancel()
	if completeErr == nil {
		if err := validateWorkerRecord(handoffID, record); err != nil {
			return worker.terminal(ctx, delivery, handoffID, "corrupt_completion", err)
		}
		return acknowledgeDelivery(ctx, delivery)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(completeErr, context.Canceled) || errors.Is(completeErr, context.DeadlineExceeded) {
		return worker.retry(ctx, delivery, handoffID, "operation_timeout", completeErr)
	}
	if errors.Is(completeErr, ErrOutcomeUnknown) || errors.Is(completeErr, ErrStoreOutcomeUnknown) {
		return worker.reconcileUnknown(ctx, delivery, handoffID)
	}
	if errors.Is(completeErr, ErrNotReady) {
		return worker.retry(ctx, delivery, handoffID, "not_ready", completeErr)
	}
	if errors.Is(completeErr, ErrRetryable) {
		return worker.retry(ctx, delivery, handoffID, "retryable_contention", completeErr)
	}
	if errors.Is(completeErr, ErrConflict) {
		return worker.terminal(ctx, delivery, handoffID, "handoff_conflict", completeErr)
	}
	if errors.Is(completeErr, ErrInvalid) {
		return worker.terminal(ctx, delivery, handoffID, "handoff_invalid", completeErr)
	}
	if errors.Is(completeErr, ErrNotFound) {
		return worker.terminal(ctx, delivery, handoffID, "handoff_not_found", completeErr)
	}
	// The public Service sanitizes unexpected storage failures to
	// ErrOutcomeUnknown. An alternate implementation is treated conservatively
	// as unknown too; no message is acknowledged on an unclassified error.
	return worker.retry(ctx, delivery, handoffID, "outcome_unknown", ErrOutcomeUnknown)
}

func (worker *Worker) reconcileUnknown(ctx context.Context, delivery Delivery, handoffID uuid.UUID) error {
	inspectCtx, cancel := context.WithTimeout(ctx, worker.config.OperationTimeout)
	record, inspectErr := worker.service.Inspect(inspectCtx, handoffID)
	cancel()
	if inspectErr == nil {
		if err := validateWorkerRecord(handoffID, record); err != nil {
			return worker.terminal(ctx, delivery, handoffID, "corrupt_completion", err)
		}
		return acknowledgeDelivery(ctx, delivery)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(inspectErr, ErrConflict) {
		return worker.terminal(ctx, delivery, handoffID, "handoff_conflict", inspectErr)
	}
	// Absent after an unknown commit does not prove that retrying under a new
	// identity is safe. The same event and same handoff ID remain retryable.
	return worker.retry(ctx, delivery, handoffID, "outcome_unknown", ErrOutcomeUnknown)
}

func validateWorkerRecord(handoffID uuid.UUID, record Record) error {
	if record.HandoffID != handoffID || record.Bundle.HandoffID != handoffID.String() {
		return errors.New("completion inspection returned another handoff identity")
	}
	if err := ValidateRecord(record); err != nil {
		return errors.New("completion inspection returned a corrupt immutable record")
	}
	return nil
}

func acknowledgeDelivery(ctx context.Context, delivery Delivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := delivery.Ack(ctx); err != nil {
		return fmt.Errorf("acknowledge completed qualification handoff: %w", err)
	}
	return nil
}

func (worker *Worker) retry(
	ctx context.Context,
	delivery Delivery,
	handoffID uuid.UUID,
	reason string,
	cause error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	attempt := delivery.Attempt()
	if attempt == 0 {
		attempt = 1
	}
	if attempt >= uint64(worker.config.TerminalAttemptThreshold) {
		return worker.terminal(ctx, delivery, handoffID, "retry_exhausted_"+reason, cause)
	}
	delay := worker.config.RetryBackOff[min(int(attempt-1), len(worker.config.RetryBackOff)-1)]
	if err := delivery.Nak(ctx, delay); err != nil {
		return fmt.Errorf("schedule same-ID qualification handoff retry: %w", err)
	}
	return nil
}

func (worker *Worker) terminal(
	ctx context.Context,
	delivery Delivery,
	handoffID uuid.UUID,
	reason string,
	cause error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := worker.quarantineRecord(delivery, handoffID, reason, cause)
	terminalCtx, cancel := context.WithTimeout(ctx, worker.config.TerminalTimeout)
	err := worker.quarantine.Quarantine(terminalCtx, record)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := worker.config.RetryBackOff[0]
		nakErr := delivery.Nak(ctx, delay)
		return errors.Join(
			fmt.Errorf("quarantine terminal qualification handoff delivery: %w", err),
			wrapIfError("schedule quarantine retry", nakErr),
		)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := delivery.Term(ctx); err != nil {
		return fmt.Errorf("terminate quarantined qualification handoff delivery: %w", err)
	}
	return nil
}

func wrapIfError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

func (worker *Worker) quarantineRecord(
	delivery Delivery,
	handoffID uuid.UUID,
	reason string,
	cause error,
) QuarantineRecord {
	payload := delivery.Payload()
	payloadHash := sha256.Sum256(payload)
	retainedPayload := payload
	payloadTruncated := false
	if len(retainedPayload) > maximumTerminalPayloadBytes {
		retainedPayload = retainedPayload[:maximumTerminalPayloadBytes]
		payloadTruncated = true
	}
	subject, subjectTruncated := boundedTerminalString(delivery.Subject(), maximumTerminalSubjectBytes)
	messageID, messageIDTruncated := boundedTerminalString(delivery.MessageID(), maximumTerminalMessageIDBytes)
	eventTypes, eventTypesTruncated := boundedTerminalEventTypes(delivery.EventTypes())
	var handoff *string
	if handoffID != uuid.Nil {
		value := handoffID.String()
		handoff = &value
	}
	detail := "terminal qualification handoff delivery"
	if cause != nil {
		detail = cause.Error()
	}
	if len(detail) > maximumTerminalDetailBytes {
		detail = detail[:maximumTerminalDetailBytes]
	}
	return QuarantineRecord{
		SchemaVersion:   HandoffQuarantineSchemaV1,
		SourceMessageID: messageID, SourceMessageIDTruncated: messageIDTruncated,
		SourceStreamSequence: delivery.StreamSequence(),
		SourceSubject:        subject, SourceSubjectTruncated: subjectTruncated,
		SourceEventTypes: eventTypes, SourceEventTypesTruncated: eventTypesTruncated,
		SourcePayloadBase64: base64.StdEncoding.EncodeToString(retainedPayload),
		SourcePayloadHash:   "sha256:" + hex.EncodeToString(payloadHash[:]),
		SourcePayloadSize:   len(payload), SourcePayloadTruncated: payloadTruncated,
		DeliveryAttempt: delivery.Attempt(), HandoffID: handoff,
		Reason: reason, Detail: detail, QuarantinedAt: worker.now().UTC().Format(time.RFC3339Nano),
	}
}

func boundedTerminalString(value string, maximum int) (string, bool) {
	if len(value) <= maximum {
		return value, false
	}
	return value[:maximum], true
}

func boundedTerminalEventTypes(values []string) ([]string, bool) {
	maximumCount := min(len(values), maximumTerminalEventTypeCount)
	retained := make([]string, 0, maximumCount)
	truncated := len(values) > maximumTerminalEventTypeCount
	for _, value := range values[:maximumCount] {
		bounded, valueTruncated := boundedTerminalString(value, maximumTerminalEventTypeBytes)
		retained = append(retained, bounded)
		truncated = truncated || valueTruncated
	}
	return retained, truncated
}

type pendingHandoffWire struct {
	HandoffID *string `json:"handoffId"`
}

func decodePendingHandoff(delivery Delivery) (uuid.UUID, error) {
	if delivery.Subject() != PendingHandoffSubject {
		return uuid.Nil, fmt.Errorf("unexpected subject %q", delivery.Subject())
	}
	eventTypes := delivery.EventTypes()
	if len(eventTypes) != 1 || !validEnvelopeString(eventTypes[0], maximumTerminalEventTypeBytes) ||
		eventTypes[0] != PendingHandoffEventType {
		return uuid.Nil, errors.New("pending handoff delivery requires exactly one canonical event type")
	}
	messageIDs := delivery.MessageIDs()
	if len(messageIDs) != 1 || delivery.MessageID() != messageIDs[0] || !canonicalUUIDv4(messageIDs[0]) {
		return uuid.Nil, errors.New("pending handoff delivery requires exactly one canonical UUIDv4 Nats-Msg-Id")
	}
	payload := delivery.Payload()
	if len(payload) == 0 || len(payload) > maximumPendingHandoffPayloadBytes {
		return uuid.Nil, errors.New("pending handoff payload size is invalid")
	}
	if err := scanPendingHandoffJSON(payload); err != nil {
		return uuid.Nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var wire pendingHandoffWire
	if err := decoder.Decode(&wire); err != nil {
		return uuid.Nil, fmt.Errorf("decode pending handoff payload: %w", err)
	}
	if err := requirePendingHandoffEOF(decoder); err != nil {
		return uuid.Nil, err
	}
	if wire.HandoffID == nil || strings.TrimSpace(*wire.HandoffID) != *wire.HandoffID {
		return uuid.Nil, errors.New("pending handoff payload is missing canonical handoffId")
	}
	handoffID, err := uuid.Parse(*wire.HandoffID)
	if err != nil || handoffID.String() != *wire.HandoffID || !validUUIDv4Value(handoffID) {
		return uuid.Nil, errors.New("pending handoff handoffId must be a canonical UUIDv4")
	}
	return handoffID, nil
}

func validEnvelopeString(value string, maximum int) bool {
	if !utf8.ValidString(value) || len(value) < 1 || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func canonicalUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122 && parsed.String() == value
}

func scanPendingHandoffJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := walkPendingHandoffJSON(decoder, 0); err != nil {
		return err
	}
	return requirePendingHandoffEOF(decoder)
}

func walkPendingHandoffJSON(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("pending handoff payload nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("scan pending handoff payload: %w", err)
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("scan pending handoff member: %w", err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("pending handoff object member name is invalid")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate pending handoff object member %q", name)
			}
			seen[name] = struct{}{}
			if err := walkPendingHandoffJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("pending handoff object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkPendingHandoffJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("pending handoff array is not closed")
		}
	default:
		return errors.New("pending handoff JSON delimiter is invalid")
	}
	return nil
}

func requirePendingHandoffEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("pending handoff payload contains trailing JSON")
		}
		return fmt.Errorf("scan pending handoff payload tail: %w", err)
	}
	return nil
}
