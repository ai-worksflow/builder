package workflowqualificationactivation

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
	WorkflowRunEventSubject = "worksflow.workflow.run.event"
	NodeCompletedEventType  = "node.completed"
	ActivationDurable       = "worksflow-workflow-qualification-activation-v1"

	ActivationQuarantineSubject   = "worksflow.workflow.qualification-activation.quarantine"
	ActivationQuarantineEventType = "workflow.qualification_activation.quarantined"
	ActivationQuarantineSchemaV1  = "worksflow-workflow-qualification-activation-quarantine/v1"

	maximumNodeCompletedPayloadBytes = 64 << 10
	maximumTerminalPayloadBytes      = 512
	maximumTerminalMessageIDBytes    = 128
	maximumTerminalSubjectBytes      = 256
	maximumTerminalEventTypeBytes    = 128
	maximumTerminalEventTypeCount    = 8
	maximumTerminalDetailBytes       = 512
	maximumBrokerHeaderBytes         = 4 << 10

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

// ActivationService admits only the opaque completion-event identity.
type ActivationService interface {
	Activate(context.Context, CompletionEventID) (Record, error)
	Inspect(context.Context, CompletionEventID) (Record, error)
}

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

type DeliveryConsumer interface {
	Fetch(context.Context, int) ([]Delivery, error)
	Limits() ConsumerLimits
	Close() error
}

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
	CompletionEventID         *string  `json:"completionEventId"`
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
		MaxInFlight: 8, TerminalAttemptThreshold: 20,
		OperationTimeout: 45 * time.Second, TerminalTimeout: 5 * time.Second,
		RetryBackOff: []time.Duration{
			time.Second, 5 * time.Second, 15 * time.Second,
			30 * time.Second, 60 * time.Second,
		},
	}
}

type Worker struct {
	service    ActivationService
	consumer   DeliveryConsumer
	quarantine QuarantineSink
	config     WorkerConfig
	now        func() time.Time
}

func NewWorker(
	service ActivationService,
	consumer DeliveryConsumer,
	quarantine QuarantineSink,
	config WorkerConfig,
) (*Worker, error) {
	if isNilInterface(service) || isNilInterface(consumer) || isNilInterface(quarantine) {
		return nil, errors.New("workflow qualification activation service, durable consumer, and quarantine sink are required")
	}
	if err := validateWorkerConfig(config); err != nil {
		return nil, err
	}
	limits := consumer.Limits()
	if limits.MaxDeliver != -1 {
		return nil, fmt.Errorf("activation consumer MaxDeliver=%d must be JetStream unlimited (-1)", limits.MaxDeliver)
	}
	if limits.MaxAckPending <= 0 || limits.MaxAckPending > maximumConsumerAckPending ||
		config.MaxInFlight > limits.MaxAckPending {
		return nil, fmt.Errorf("activation MaxInFlight=%d exceeds consumer MaxAckPending=%d", config.MaxInFlight, limits.MaxAckPending)
	}
	if limits.MaxRequestBatch <= 0 || limits.MaxRequestBatch > maximumConsumerRequestBatch ||
		config.MaxInFlight > limits.MaxRequestBatch {
		return nil, fmt.Errorf("activation MaxInFlight=%d exceeds consumer MaxRequestBatch=%d", config.MaxInFlight, limits.MaxRequestBatch)
	}
	minimumRequestBytes, ok := safeMultiplyInt(config.MaxInFlight, maximumNodeCompletedPayloadBytes+maximumBrokerHeaderBytes)
	if !ok || limits.MaxRequestMaxBytes < minimumRequestBytes || limits.MaxRequestMaxBytes > maximumConsumerRequestMaxBytes {
		return nil, fmt.Errorf("activation consumer MaxRequestMaxBytes=%d cannot carry the bounded in-flight batch of %d bytes", limits.MaxRequestMaxBytes, minimumRequestBytes)
	}
	if limits.AckConfirmWait <= 0 || limits.AckConfirmWait > maximumConsumerAckConfirmWait {
		return nil, fmt.Errorf("activation consumer AckConfirmWait=%s is invalid", limits.AckConfirmWait)
	}
	minimumAckWait, err := requiredConsumerAckWait(config, limits.AckConfirmWait)
	if err != nil {
		return nil, err
	}
	if limits.AckWait < minimumAckWait || limits.AckWait > maximumConsumerAckWait {
		return nil, fmt.Errorf("activation consumer AckWait=%s is shorter than bounded activate/inspect window %s", limits.AckWait, minimumAckWait)
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
		return errors.New("activation worker limits, terminal threshold, and timeouts are invalid")
	}
	if config.TerminalTimeout > maximumWorkerTerminalTimeout || len(config.RetryBackOff) > maximumWorkerRetryBackOffCount {
		return errors.New("activation worker terminal timeout or retry schedule exceeds its bound")
	}
	for _, delay := range config.RetryBackOff {
		if delay <= 0 || delay > maximumWorkerRetryBackOff {
			return errors.New("activation worker retry backoff values must be positive")
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
			return 0, errors.New("activation AckWait calculation overflowed")
		}
		total += value
	}
	if total > maximumConsumerAckWait {
		return 0, fmt.Errorf("activation required AckWait=%s exceeds the supported bound", total)
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
		return errors.New("activation worker and context are required")
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
		return 0, errors.New("activation worker and context are required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	deliveries, err := worker.consumer.Fetch(ctx, worker.config.MaxInFlight)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("fetch activation deliveries: %w", err)
	}
	if len(deliveries) > worker.config.MaxInFlight {
		return 0, errors.New("activation consumer exceeded bounded in-flight batch")
	}

	results := make(chan error, len(deliveries))
	var wait sync.WaitGroup
	for _, delivery := range deliveries {
		if isNilInterface(delivery) {
			results <- errors.New("activation consumer returned a nil delivery")
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
		return errors.New("activation worker, delivery, service, quarantine, and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	completionEventID, relevant, decodeErr := decodeNodeCompletedDelivery(delivery)
	if decodeErr != nil {
		return worker.terminal(ctx, delivery, CompletionEventID{}, "invalid_message", decodeErr)
	}
	// The workflow event subject carries all event types. A closed non-
	// node.completed event is outside this worker's authority and is ACKed only
	// after the exact envelope and its header/payload type equality are proven.
	if !relevant {
		return acknowledgeDelivery(ctx, delivery)
	}

	operationCtx, cancel := context.WithTimeout(ctx, worker.config.OperationTimeout)
	record, activationErr := worker.service.Activate(operationCtx, completionEventID)
	cancel()
	if activationErr == nil {
		if err := validateWorkerRecord(completionEventID, record); err != nil {
			return worker.terminal(ctx, delivery, completionEventID, "corrupt_activation", err)
		}
		return acknowledgeDelivery(ctx, delivery)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(activationErr, context.Canceled) || errors.Is(activationErr, context.DeadlineExceeded) {
		return worker.retry(ctx, delivery, completionEventID, "operation_timeout", activationErr)
	}
	if errors.Is(activationErr, ErrOutcomeUnknown) || errors.Is(activationErr, ErrStoreOutcomeUnknown) {
		return worker.reconcileUnknown(ctx, delivery, completionEventID)
	}
	if errors.Is(activationErr, ErrNotReady) {
		return worker.retry(ctx, delivery, completionEventID, "not_ready", activationErr)
	}
	if errors.Is(activationErr, ErrRetryable) {
		return worker.retry(ctx, delivery, completionEventID, "retryable_contention", activationErr)
	}
	if errors.Is(activationErr, ErrConflict) {
		return worker.terminal(ctx, delivery, completionEventID, "activation_conflict", activationErr)
	}
	if errors.Is(activationErr, ErrInvalid) {
		return worker.terminal(ctx, delivery, completionEventID, "activation_invalid", activationErr)
	}
	if errors.Is(activationErr, ErrNotFound) {
		return worker.terminal(ctx, delivery, completionEventID, "activation_not_found", activationErr)
	}
	return worker.retry(ctx, delivery, completionEventID, "outcome_unknown", ErrOutcomeUnknown)
}

func (worker *Worker) reconcileUnknown(ctx context.Context, delivery Delivery, completionEventID CompletionEventID) error {
	inspectCtx, cancel := context.WithTimeout(ctx, worker.config.OperationTimeout)
	record, inspectErr := worker.service.Inspect(inspectCtx, completionEventID)
	cancel()
	if inspectErr == nil {
		if err := validateWorkerRecord(completionEventID, record); err != nil {
			return worker.terminal(ctx, delivery, completionEventID, "corrupt_activation", err)
		}
		return acknowledgeDelivery(ctx, delivery)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(inspectErr, ErrConflict) {
		return worker.terminal(ctx, delivery, completionEventID, "activation_conflict", inspectErr)
	}
	return worker.retry(ctx, delivery, completionEventID, "outcome_unknown", ErrOutcomeUnknown)
}

func validateWorkerRecord(completionEventID CompletionEventID, record Record) error {
	if record.CompletionEventID != completionEventID {
		return errors.New("activation returned another completion event identity")
	}
	switch record.Disposition {
	case DispositionIgnored:
		want := ignoredRecord(completionEventID)
		if record != want {
			return errors.New("non-target activation returned authority facts")
		}
	case DispositionActivated:
		if record.OperationID.Version() != 4 || record.AuthorityID.Version() != 4 ||
			record.WorkflowRunID.Version() != 4 || record.NodeRunID.Version() != 4 ||
			record.ActivationEventID.Version() != 4 || record.ActivationEventSequence < 1 ||
			record.ActivationEventSequence > maximumSafeInteger || !validDigest(record.RequestHash) ||
			!validDigest(record.TargetHash) || !validDigest(record.InputHash) || !validDigest(record.AuthorityHash) {
			return errors.New("activation returned an invalid immutable projection")
		}
	default:
		return errors.New("activation returned an unknown disposition")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && strings.ToLower(value) == value
}

func acknowledgeDelivery(ctx context.Context, delivery Delivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := delivery.Ack(ctx); err != nil {
		return fmt.Errorf("acknowledge workflow qualification activation: %w", err)
	}
	return nil
}

func (worker *Worker) retry(
	ctx context.Context,
	delivery Delivery,
	completionEventID CompletionEventID,
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
		return worker.terminal(ctx, delivery, completionEventID, "retry_exhausted_"+reason, cause)
	}
	delay := worker.config.RetryBackOff[min(int(attempt-1), len(worker.config.RetryBackOff)-1)]
	if err := delivery.Nak(ctx, delay); err != nil {
		return fmt.Errorf("schedule same-event activation retry: %w", err)
	}
	return nil
}

func (worker *Worker) terminal(
	ctx context.Context,
	delivery Delivery,
	completionEventID CompletionEventID,
	reason string,
	cause error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := worker.quarantineRecord(delivery, completionEventID, reason, cause)
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
			fmt.Errorf("quarantine terminal activation delivery: %w", err),
			wrapIfError("schedule quarantine retry", nakErr),
		)
	}
	if err := delivery.Term(ctx); err != nil {
		return fmt.Errorf("terminate quarantined activation delivery: %w", err)
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
	completionEventID CompletionEventID,
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
	var completionID *string
	if completionEventID.valid() {
		value := completionEventID.String()
		completionID = &value
	}
	detail := "terminal workflow qualification activation delivery"
	if cause != nil {
		detail = cause.Error()
	}
	if len(detail) > maximumTerminalDetailBytes {
		detail = detail[:maximumTerminalDetailBytes]
	}
	return QuarantineRecord{
		SchemaVersion:   ActivationQuarantineSchemaV1,
		SourceMessageID: messageID, SourceMessageIDTruncated: messageIDTruncated,
		SourceStreamSequence: delivery.StreamSequence(),
		SourceSubject:        subject, SourceSubjectTruncated: subjectTruncated,
		SourceEventTypes: eventTypes, SourceEventTypesTruncated: eventTypesTruncated,
		SourcePayloadBase64: base64.StdEncoding.EncodeToString(retainedPayload),
		SourcePayloadHash:   "sha256:" + hex.EncodeToString(payloadHash[:]),
		SourcePayloadSize:   len(payload), SourcePayloadTruncated: payloadTruncated,
		DeliveryAttempt: delivery.Attempt(), CompletionEventID: completionID,
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

type workflowEventWire struct {
	ID         *string         `json:"id"`
	ProjectID  *string         `json:"projectId"`
	RunID      *string         `json:"runId"`
	Sequence   *int64          `json:"sequence"`
	Type       *string         `json:"type"`
	OccurredAt *string         `json:"occurredAt"`
	Payload    json.RawMessage `json:"payload"`
	NodeKey    *string         `json:"nodeKey"`
	ActorID    *string         `json:"actorId,omitempty"`
}

func decodeNodeCompletedDelivery(delivery Delivery) (CompletionEventID, bool, error) {
	if delivery.Subject() != WorkflowRunEventSubject {
		return CompletionEventID{}, false, fmt.Errorf("unexpected workflow event subject %q", delivery.Subject())
	}
	eventTypes := delivery.EventTypes()
	if len(eventTypes) != 1 || !validEnvelopeString(eventTypes[0], maximumTerminalEventTypeBytes) {
		return CompletionEventID{}, false, errors.New("workflow event delivery requires exactly one canonical event type")
	}
	payload := delivery.Payload()
	if len(payload) == 0 || len(payload) > maximumNodeCompletedPayloadBytes {
		return CompletionEventID{}, false, errors.New("workflow event envelope size is invalid")
	}
	if err := scanExactJSON(payload); err != nil {
		return CompletionEventID{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var wire workflowEventWire
	if err := decoder.Decode(&wire); err != nil {
		return CompletionEventID{}, false, fmt.Errorf("decode workflow event envelope: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return CompletionEventID{}, false, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil || members == nil {
		return CompletionEventID{}, false, errors.New("workflow event envelope must be an object")
	}
	for _, required := range []string{"id", "projectId", "runId", "sequence", "type", "occurredAt", "payload"} {
		if _, exists := members[required]; !exists {
			return CompletionEventID{}, false, errors.New("workflow event envelope is incomplete")
		}
	}
	for _, optional := range []string{"nodeKey", "actorId"} {
		if raw, exists := members[optional]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return CompletionEventID{}, false, fmt.Errorf("workflow event %s cannot be null", optional)
		}
	}
	if wire.ID == nil || wire.ProjectID == nil || wire.RunID == nil || wire.Sequence == nil ||
		wire.Type == nil || wire.OccurredAt == nil || wire.Payload == nil {
		return CompletionEventID{}, false, errors.New("workflow event envelope has a null required member")
	}
	completionEventID, err := ParseCompletionEventID(*wire.ID)
	if err != nil {
		return CompletionEventID{}, false, err
	}
	messageIDs := delivery.MessageIDs()
	if len(messageIDs) != 1 || messageIDs[0] != completionEventID.String() || delivery.MessageID() != messageIDs[0] {
		return CompletionEventID{}, false, errors.New("workflow event requires one Nats-Msg-Id equal to its event ID")
	}
	if !canonicalUUIDv4(*wire.ProjectID) || !canonicalUUIDv4(*wire.RunID) ||
		*wire.Sequence < 1 || *wire.Sequence > maximumSafeInteger || !validEnvelopeString(*wire.Type, maximumTerminalEventTypeBytes) ||
		*wire.Type != eventTypes[0] {
		return CompletionEventID{}, false, errors.New("workflow event envelope identity, sequence, or type is invalid or differs from its header")
	}
	if wire.NodeKey != nil && !validEnvelopeString(*wire.NodeKey, 256) {
		return CompletionEventID{}, false, errors.New("workflow event node key is invalid")
	}
	if *wire.Type == NodeCompletedEventType && wire.NodeKey == nil {
		return CompletionEventID{}, false, errors.New("node.completed envelope requires a node key")
	}
	if wire.ActorID != nil && !canonicalUUIDv4(*wire.ActorID) {
		return CompletionEventID{}, false, errors.New("workflow event actor identity is invalid")
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, *wire.OccurredAt)
	if err != nil || parsedTime.Location() != time.UTC || !strings.HasSuffix(*wire.OccurredAt, "Z") {
		return CompletionEventID{}, false, errors.New("workflow event occurrence time is invalid")
	}
	var eventPayload map[string]json.RawMessage
	if err := json.Unmarshal(wire.Payload, &eventPayload); err != nil || eventPayload == nil {
		return CompletionEventID{}, false, errors.New("workflow event payload must be a JSON object")
	}
	return completionEventID, *wire.Type == NodeCompletedEventType, nil
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

func scanExactJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := walkExactJSON(decoder, 0); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func walkExactJSON(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("workflow event JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("scan workflow event JSON: %w", err)
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
				return fmt.Errorf("scan workflow event member: %w", err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("workflow event object member name is invalid")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate workflow event object member %q", name)
			}
			seen[name] = struct{}{}
			if err := walkExactJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("workflow event object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkExactJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("workflow event array is not closed")
		}
	default:
		return errors.New("workflow event JSON delimiter is invalid")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow event envelope contains trailing JSON")
		}
		return fmt.Errorf("scan workflow event JSON tail: %w", err)
	}
	return nil
}
