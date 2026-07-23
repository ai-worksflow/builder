package qualificationhandoff

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

const (
	DefaultHandoffEventStream = "WORKSFLOW_EVENTS"
	handoffEventTypeHeader    = "Worksflow-Event-Type"
	handoffSourceIDHeader     = "Worksflow-Source-Message-ID"
	unlimitedNATSDeliveries   = -1

	maximumNATSStreamNameBytes   = 128
	maximumNATSDurableNameBytes  = 128
	maximumNATSAckBackOffCount   = 64
	maximumNATSAckBackOff        = time.Hour
	maximumNATSRequestExpires    = time.Minute
	maximumNATSFetchWait         = time.Minute
	maximumNATSMaxWaiting        = 1024
	maximumNATSMaxAckPending     = maximumConsumerAckPending
	maximumQuarantinePublishWait = time.Minute
	maximumQuarantineWireBytes   = 16 << 10
	minimumHandoffRetention      = 30 * 24 * time.Hour
	minimumHandoffDedupe         = 10 * time.Minute
)

type NATSConsumerConfig struct {
	Stream             string
	Durable            string
	AckWait            time.Duration
	AckBackOff         []time.Duration
	MaxAckPending      int
	MaxRequestBatch    int
	MaxRequestMaxBytes int
	MaxRequestExpires  time.Duration
	MaxWaiting         int
	FetchWait          time.Duration
	AckConfirmWait     time.Duration
}

func DefaultNATSConsumerConfig() NATSConsumerConfig {
	return NATSConsumerConfig{
		Stream: DefaultHandoffEventStream, Durable: PendingHandoffDurable,
		AckWait: 2 * time.Minute,
		AckBackOff: []time.Duration{
			2 * time.Minute,
			5 * time.Minute,
			10 * time.Minute,
		},
		MaxAckPending: 8, MaxRequestBatch: 8, MaxRequestMaxBytes: 1 << 20,
		MaxRequestExpires: 2 * time.Second, MaxWaiting: 16,
		FetchWait: 2 * time.Second, AckConfirmWait: 5 * time.Second,
	}
}

// NATSPostureRequirements is the closed deployment contract that must be
// proven before this package creates or binds its durable consumer.
type NATSPostureRequirements struct {
	Stream                   string
	SourceSubject            string
	QuarantineSubject        string
	Durable                  string
	MaximumEventPayloadBytes int
	MaximumQuarantineBytes   int
	MinimumStreamMaxMsgBytes int
	RequiredStorage          nats.StorageType
	RequiredRetention        nats.RetentionPolicy
	RequiredDiscard          nats.DiscardPolicy
	MinimumRetention         time.Duration
	MinimumDuplicateWindow   time.Duration
	MinimumReplicas          int
	RequiredMaxDeliver       int
	RequiredAckPolicy        nats.AckPolicy
	RequiredDeliverPolicy    nats.DeliverPolicy
}

// NATSReadiness is injected because stream subjects, durable storage,
// replication, retention, deduplication, and account ACLs are deployment
// authority. Missing or failed posture proof blocks consumer startup.
type NATSReadiness interface {
	AssertReady(context.Context, NATSPostureRequirements) error
}

type natsConsumerAPI interface {
	ConsumerInfo(string, string, ...nats.JSOpt) (*nats.ConsumerInfo, error)
	AddConsumer(string, *nats.ConsumerConfig, ...nats.JSOpt) (*nats.ConsumerInfo, error)
	PullSubscribe(string, string, ...nats.SubOpt) (*nats.Subscription, error)
}

type natsQuarantinePublisher interface {
	PublishMsg(*nats.Msg, ...nats.PubOpt) (*nats.PubAck, error)
}

type NATSConsumer struct {
	subscription *nats.Subscription
	config       NATSConsumerConfig
}

func NewNATSConsumer(
	ctx context.Context,
	jetStream natsConsumerAPI,
	readiness NATSReadiness,
	config NATSConsumerConfig,
) (*NATSConsumer, error) {
	if isNilInterface(ctx) || isNilInterface(jetStream) || isNilInterface(readiness) {
		return nil, errors.New("context, JetStream, and qualification handoff stream readiness are required")
	}
	if err := validateNATSConsumerConfig(config); err != nil {
		return nil, err
	}
	posture := NATSPostureRequirements{
		Stream: config.Stream, SourceSubject: PendingHandoffSubject,
		QuarantineSubject: HandoffQuarantineSubject, Durable: PendingHandoffDurable,
		MaximumEventPayloadBytes: maximumPendingHandoffPayloadBytes,
		MaximumQuarantineBytes:   maximumQuarantineWireBytes,
		MinimumStreamMaxMsgBytes: max(maximumPendingHandoffPayloadBytes, maximumQuarantineWireBytes) + maximumBrokerHeaderBytes,
		RequiredStorage:          nats.FileStorage, RequiredRetention: nats.LimitsPolicy,
		RequiredDiscard: nats.DiscardOld, MinimumRetention: minimumHandoffRetention,
		MinimumDuplicateWindow: minimumHandoffDedupe, MinimumReplicas: 1,
		RequiredMaxDeliver: unlimitedNATSDeliveries,
		RequiredAckPolicy:  nats.AckExplicitPolicy, RequiredDeliverPolicy: nats.DeliverAllPolicy,
	}
	if err := readiness.AssertReady(ctx, posture); err != nil {
		return nil, fmt.Errorf("qualification handoff stream posture is not ready: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ensureNATSHandoffConsumer(ctx, jetStream, config); err != nil {
		return nil, err
	}
	subscription, err := jetStream.PullSubscribe(
		PendingHandoffSubject,
		config.Durable,
		nats.Bind(config.Stream, config.Durable),
		nats.ManualAck(),
		nats.Context(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("bind qualification handoff durable consumer: %w", err)
	}
	if subscription == nil {
		return nil, errors.New("bind qualification handoff durable consumer returned no subscription")
	}
	return &NATSConsumer{subscription: subscription, config: cloneNATSConsumerConfig(config)}, nil
}

func validateNATSConsumerConfig(config NATSConsumerConfig) error {
	if !validEnvelopeString(config.Stream, maximumNATSStreamNameBytes) ||
		!validEnvelopeString(config.Durable, maximumNATSDurableNameBytes) ||
		config.Durable != PendingHandoffDurable {
		return errors.New("qualification handoff stream is required and durable name must use the frozen v1 identity")
	}
	if config.AckWait <= 0 || config.AckWait > maximumConsumerAckWait ||
		len(config.AckBackOff) == 0 || len(config.AckBackOff) > maximumNATSAckBackOffCount ||
		config.MaxAckPending <= 0 || config.MaxAckPending > maximumNATSMaxAckPending ||
		config.MaxRequestBatch <= 0 || config.MaxRequestBatch > maximumConsumerRequestBatch ||
		config.MaxRequestBatch > config.MaxAckPending || config.MaxRequestExpires <= 0 ||
		config.MaxRequestExpires > maximumNATSRequestExpires ||
		config.MaxRequestMaxBytes <= 0 || config.MaxRequestMaxBytes > maximumConsumerRequestMaxBytes ||
		config.MaxWaiting <= 0 || config.MaxWaiting > maximumNATSMaxWaiting || config.FetchWait <= 0 ||
		config.FetchWait > maximumNATSFetchWait ||
		config.FetchWait > config.MaxRequestExpires || config.AckConfirmWait <= 0 {
		return errors.New("qualification handoff JetStream consumer limits and timeouts are invalid")
	}
	minimumRequestBytes, ok := safeMultiplyInt(config.MaxRequestBatch, maximumPendingHandoffPayloadBytes+maximumBrokerHeaderBytes)
	if !ok || config.MaxRequestMaxBytes < minimumRequestBytes ||
		config.AckConfirmWait > maximumConsumerAckConfirmWait || config.AckBackOff[0] < config.AckWait {
		return errors.New("qualification handoff request bytes, AckConfirmWait, or ack backoff are narrower than the bounded contract")
	}
	previous := time.Duration(0)
	for _, delay := range config.AckBackOff {
		if delay <= 0 || delay > maximumNATSAckBackOff || delay < previous {
			return errors.New("qualification handoff ack backoff must be positive and nondecreasing")
		}
		previous = delay
	}
	return nil
}

func ensureNATSHandoffConsumer(
	ctx context.Context,
	jetStream natsConsumerAPI,
	config NATSConsumerConfig,
) error {
	desired := desiredNATSConsumerConfig(config)
	info, err := jetStream.ConsumerInfo(config.Stream, config.Durable, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		info, err = jetStream.AddConsumer(config.Stream, &desired, nats.Context(ctx))
	}
	if err != nil {
		return fmt.Errorf("ensure qualification handoff durable consumer: %w", err)
	}
	if info == nil {
		return errors.New("ensure qualification handoff durable consumer returned no configuration")
	}
	if info.Stream != config.Stream || info.Name != config.Durable {
		return errors.New("existing qualification handoff durable consumer is bound to another stream or name")
	}
	if err := validateExistingNATSConsumer(info, desired); err != nil {
		return err
	}
	return nil
}

func desiredNATSConsumerConfig(config NATSConsumerConfig) nats.ConsumerConfig {
	return nats.ConsumerConfig{
		Durable: config.Durable, DeliverPolicy: nats.DeliverAllPolicy,
		AckPolicy: nats.AckExplicitPolicy, AckWait: config.AckWait,
		MaxDeliver: unlimitedNATSDeliveries, BackOff: slices.Clone(config.AckBackOff),
		FilterSubject: PendingHandoffSubject, ReplayPolicy: nats.ReplayInstantPolicy,
		MaxWaiting: config.MaxWaiting, MaxAckPending: config.MaxAckPending,
		MaxRequestBatch:    config.MaxRequestBatch,
		MaxRequestMaxBytes: config.MaxRequestMaxBytes,
		MaxRequestExpires:  config.MaxRequestExpires,
	}
}

func validateExistingNATSConsumer(info *nats.ConsumerInfo, desired nats.ConsumerConfig) error {
	actual := info.Config
	if info.Stream == "" || actual.Durable != desired.Durable || actual.Name != "" && actual.Name != desired.Durable ||
		actual.DeliverPolicy != desired.DeliverPolicy || actual.AckPolicy != desired.AckPolicy ||
		actual.AckWait != desired.AckWait || actual.MaxDeliver != desired.MaxDeliver ||
		!slices.Equal(actual.BackOff, desired.BackOff) || actual.FilterSubject != desired.FilterSubject ||
		len(actual.FilterSubjects) != 0 || actual.ReplayPolicy != desired.ReplayPolicy ||
		actual.MaxWaiting != desired.MaxWaiting || actual.MaxAckPending != desired.MaxAckPending ||
		actual.MaxRequestBatch != desired.MaxRequestBatch ||
		actual.MaxRequestMaxBytes != desired.MaxRequestMaxBytes ||
		actual.MaxRequestExpires != desired.MaxRequestExpires ||
		actual.DeliverSubject != "" || actual.DeliverGroup != "" || actual.FlowControl || actual.Heartbeat != 0 ||
		actual.HeadersOnly {
		return errors.New("existing qualification handoff durable consumer does not match the frozen pull/manual-ack policy")
	}
	return nil
}

func cloneNATSConsumerConfig(config NATSConsumerConfig) NATSConsumerConfig {
	cloned := config
	cloned.AckBackOff = slices.Clone(config.AckBackOff)
	return cloned
}

func (consumer *NATSConsumer) Limits() ConsumerLimits {
	if consumer == nil {
		return ConsumerLimits{}
	}
	return ConsumerLimits{
		MaxDeliver:         unlimitedNATSDeliveries,
		MaxAckPending:      consumer.config.MaxAckPending,
		AckWait:            consumer.config.AckWait,
		AckConfirmWait:     consumer.config.AckConfirmWait,
		MaxRequestBatch:    consumer.config.MaxRequestBatch,
		MaxRequestMaxBytes: consumer.config.MaxRequestMaxBytes,
	}
}

func (consumer *NATSConsumer) Fetch(ctx context.Context, maximum int) ([]Delivery, error) {
	if consumer == nil || consumer.subscription == nil || isNilInterface(ctx) || maximum <= 0 ||
		maximum > consumer.config.MaxRequestBatch || maximum > consumer.config.MaxAckPending {
		return nil, errors.New("qualification handoff pull request is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, consumer.config.FetchWait)
	messages, err := consumer.subscription.Fetch(
		maximum,
		nats.Context(fetchCtx),
		nats.PullMaxBytes(consumer.config.MaxRequestMaxBytes),
	)
	cancel()
	if err != nil && len(messages) == 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil
		}
		return nil, err
	}
	deliveries := make([]Delivery, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			return nil, errors.New("qualification handoff pull returned a nil NATS message")
		}
		metadata, metadataErr := message.Metadata()
		if metadataErr != nil {
			return nil, fmt.Errorf("read qualification handoff delivery metadata: %w", metadataErr)
		}
		deliveries = append(deliveries, &natsDelivery{
			message: message, attempt: metadata.NumDelivered,
			streamSequence: metadata.Sequence.Stream,
			ackConfirmWait: consumer.config.AckConfirmWait,
		})
	}
	return deliveries, nil
}

func (consumer *NATSConsumer) Close() error {
	if consumer == nil || consumer.subscription == nil {
		return nil
	}
	return consumer.subscription.Unsubscribe()
}

type natsDelivery struct {
	message        *nats.Msg
	attempt        uint64
	streamSequence uint64
	ackConfirmWait time.Duration
}

func (delivery *natsDelivery) Subject() string {
	if delivery == nil || delivery.message == nil {
		return ""
	}
	return delivery.message.Subject
}

func (delivery *natsDelivery) EventTypes() []string {
	if delivery == nil || delivery.message == nil || delivery.message.Header == nil {
		return nil
	}
	return slices.Clone(delivery.message.Header.Values(handoffEventTypeHeader))
}

func (delivery *natsDelivery) Payload() []byte {
	if delivery == nil || delivery.message == nil {
		return nil
	}
	return delivery.message.Data
}

func (delivery *natsDelivery) MessageID() string {
	if delivery == nil || delivery.message == nil || delivery.message.Header == nil {
		return ""
	}
	return delivery.message.Header.Get(nats.MsgIdHdr)
}

func (delivery *natsDelivery) MessageIDs() []string {
	if delivery == nil || delivery.message == nil || delivery.message.Header == nil {
		return nil
	}
	return slices.Clone(delivery.message.Header.Values(nats.MsgIdHdr))
}

func (delivery *natsDelivery) StreamSequence() uint64 {
	if delivery == nil {
		return 0
	}
	return delivery.streamSequence
}

func (delivery *natsDelivery) Attempt() uint64 {
	if delivery == nil {
		return 0
	}
	return delivery.attempt
}

func (delivery *natsDelivery) Ack(ctx context.Context) error {
	if delivery == nil || delivery.message == nil || isNilInterface(ctx) {
		return errors.New("NATS qualification handoff delivery and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ackCtx, cancel := context.WithTimeout(ctx, delivery.ackConfirmWait)
	defer cancel()
	return delivery.message.AckSync(nats.Context(ackCtx))
}

func (delivery *natsDelivery) Nak(ctx context.Context, delay time.Duration) error {
	if delivery == nil || delivery.message == nil || isNilInterface(ctx) || delay <= 0 {
		return errors.New("NATS qualification handoff delivery, context, and retry delay are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return delivery.message.NakWithDelay(delay, nats.Context(ctx))
}

func (delivery *natsDelivery) Term(ctx context.Context) error {
	if delivery == nil || delivery.message == nil || isNilInterface(ctx) {
		return errors.New("NATS qualification handoff delivery and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return delivery.message.Term(nats.Context(ctx))
}

type NATSQuarantineSink struct {
	publisher   natsQuarantinePublisher
	publishWait time.Duration
}

func NewNATSQuarantineSink(
	publisher natsQuarantinePublisher,
	publishWait time.Duration,
) (*NATSQuarantineSink, error) {
	if isNilInterface(publisher) || publishWait <= 0 || publishWait > maximumQuarantinePublishWait {
		return nil, errors.New("JetStream quarantine publisher and bounded positive publish timeout are required")
	}
	return &NATSQuarantineSink{publisher: publisher, publishWait: publishWait}, nil
}

func (sink *NATSQuarantineSink) Quarantine(ctx context.Context, record QuarantineRecord) error {
	if sink == nil || isNilInterface(sink.publisher) || isNilInterface(ctx) {
		return errors.New("qualification handoff quarantine sink and context are required")
	}
	if err := validateQuarantineRecord(record); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode qualification handoff quarantine record: %w", err)
	}
	message := nats.NewMsg(HandoffQuarantineSubject)
	message.Data = encoded
	message.Header.Set(nats.MsgIdHdr, quarantineMessageID(record))
	message.Header.Set(handoffEventTypeHeader, HandoffQuarantineEventType)
	if record.SourceMessageID != "" {
		message.Header.Set(handoffSourceIDHeader, record.SourceMessageID)
	}
	publishCtx, cancel := context.WithTimeout(ctx, sink.publishWait)
	defer cancel()
	if _, err := sink.publisher.PublishMsg(message, nats.Context(publishCtx)); err != nil {
		return fmt.Errorf("publish qualification handoff quarantine record: %w", err)
	}
	return nil
}

func validateQuarantineRecord(record QuarantineRecord) error {
	if record.SchemaVersion != HandoffQuarantineSchemaV1 ||
		!strings.HasPrefix(record.SourcePayloadHash, "sha256:") || len(record.SourcePayloadHash) != 71 ||
		len(record.SourceMessageID) > maximumTerminalMessageIDBytes ||
		len(record.SourceSubject) > maximumTerminalSubjectBytes ||
		len(record.SourceEventTypes) > maximumTerminalEventTypeCount ||
		record.SourcePayloadSize < 0 ||
		record.DeliveryAttempt == 0 || record.Reason == "" || record.Detail == "" ||
		len(record.Detail) > maximumTerminalDetailBytes ||
		record.QuarantinedAt == "" || record.SourceMessageID == "" && record.SourceStreamSequence == 0 {
		return errors.New("qualification handoff quarantine record is incomplete")
	}
	if !validQuarantineReason(record.Reason) {
		return errors.New("qualification handoff quarantine reason is invalid")
	}
	payload, err := base64.StdEncoding.DecodeString(record.SourcePayloadBase64)
	if err != nil {
		return errors.New("qualification handoff quarantine payload encoding is invalid")
	}
	if len(payload) > maximumTerminalPayloadBytes || len(payload) > record.SourcePayloadSize ||
		record.SourcePayloadTruncated != (len(payload) < record.SourcePayloadSize) {
		return errors.New("qualification handoff quarantine payload bounds are invalid")
	}
	if !record.SourcePayloadTruncated {
		digest := sha256.Sum256(payload)
		if record.SourcePayloadHash != "sha256:"+hex.EncodeToString(digest[:]) {
			return errors.New("qualification handoff quarantine payload hash is invalid")
		}
	} else if _, err := hex.DecodeString(strings.TrimPrefix(record.SourcePayloadHash, "sha256:")); err != nil {
		return errors.New("qualification handoff quarantine payload hash is invalid")
	}
	for _, eventType := range record.SourceEventTypes {
		if len(eventType) > maximumTerminalEventTypeBytes {
			return errors.New("qualification handoff quarantine event type bounds are invalid")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, record.QuarantinedAt); err != nil {
		return errors.New("qualification handoff quarantine timestamp is invalid")
	}
	if record.HandoffID != nil {
		parsed, err := uuidParseCanonicalV4(*record.HandoffID)
		if err != nil || parsed == "" {
			return errors.New("qualification handoff quarantine handoffId is invalid")
		}
	}
	return nil
}

func validQuarantineReason(reason string) bool {
	switch reason {
	case "invalid_message", "corrupt_completion", "handoff_conflict", "handoff_invalid", "handoff_not_found",
		"retry_exhausted_operation_timeout", "retry_exhausted_not_ready",
		"retry_exhausted_retryable_contention", "retry_exhausted_outcome_unknown":
		return true
	default:
		return false
	}
}

func uuidParseCanonicalV4(value string) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || !validUUIDv4Value(parsed) {
		return "", errors.New("value is not a canonical UUIDv4")
	}
	return parsed.String(), nil
}

func quarantineMessageID(record QuarantineRecord) string {
	identity := fmt.Sprintf(
		"%s\x00%d\x00%s\x00%s",
		record.SourceMessageID,
		record.SourceStreamSequence,
		record.SourcePayloadHash,
		record.SourceSubject,
	)
	digest := sha256.Sum256([]byte(identity))
	return "qualification-handoff-quarantine-" + hex.EncodeToString(digest[:])
}
