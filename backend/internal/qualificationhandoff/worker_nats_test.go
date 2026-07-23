package qualificationhandoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type natsConsumerAPIFake struct {
	info         *nats.ConsumerInfo
	infoErr      error
	addErr       error
	addedStream  string
	addedConfig  *nats.ConsumerConfig
	infoCalls    int
	addCalls     int
	pullCalls    int
	pullErr      error
	subscription *nats.Subscription
}

type natsReadinessFake struct {
	err         error
	calls       int
	requirement NATSPostureRequirements
}

func (readiness *natsReadinessFake) AssertReady(_ context.Context, requirement NATSPostureRequirements) error {
	readiness.calls++
	readiness.requirement = requirement
	return readiness.err
}

func (api *natsConsumerAPIFake) ConsumerInfo(_ string, _ string, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	api.infoCalls++
	return api.info, api.infoErr
}

func (api *natsConsumerAPIFake) AddConsumer(stream string, config *nats.ConsumerConfig, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	api.addCalls++
	api.addedStream = stream
	if config != nil {
		copy := *config
		copy.BackOff = slices.Clone(config.BackOff)
		api.addedConfig = &copy
	}
	if api.addErr != nil {
		return nil, api.addErr
	}
	return &nats.ConsumerInfo{Stream: stream, Name: config.Durable, Config: *config}, nil
}

func (api *natsConsumerAPIFake) PullSubscribe(_ string, _ string, _ ...nats.SubOpt) (*nats.Subscription, error) {
	api.pullCalls++
	return api.subscription, api.pullErr
}

type natsPublisherFake struct {
	message *nats.Msg
	err     error
	calls   int
}

func (publisher *natsPublisherFake) PublishMsg(message *nats.Msg, _ ...nats.PubOpt) (*nats.PubAck, error) {
	publisher.calls++
	publisher.message = message
	if publisher.err != nil {
		return nil, publisher.err
	}
	return &nats.PubAck{Stream: DefaultHandoffEventStream, Sequence: 1}, nil
}

func TestDefaultNATSConsumerConfigIsBoundedAndFrozen(t *testing.T) {
	config := DefaultNATSConsumerConfig()
	if err := validateNATSConsumerConfig(config); err != nil {
		t.Fatal(err)
	}
	desired := desiredNATSConsumerConfig(config)
	if config.Durable != PendingHandoffDurable || desired.FilterSubject != PendingHandoffSubject ||
		desired.AckPolicy != nats.AckExplicitPolicy || desired.DeliverPolicy != nats.DeliverAllPolicy ||
		desired.DeliverSubject != "" || desired.MaxDeliver != unlimitedNATSDeliveries || desired.MaxAckPending != 8 ||
		desired.MaxRequestBatch != 8 || desired.MaxRequestMaxBytes != config.MaxRequestMaxBytes || len(desired.BackOff) == 0 {
		t.Fatalf("default durable consumer = %#v", desired)
	}
	config.AckBackOff[0] = time.Millisecond
	if desired.BackOff[0] == time.Millisecond {
		t.Fatal("desired consumer retained a mutable caller backoff slice")
	}
}

func TestNewNATSConsumerRequiresStreamPostureBeforeConsumerAccess(t *testing.T) {
	config := DefaultNATSConsumerConfig()
	api := &natsConsumerAPIFake{}
	if _, err := NewNATSConsumer(context.Background(), api, nil, config); err == nil || api.infoCalls != 0 {
		t.Fatalf("nil readiness error=%v infoCalls=%d", err, api.infoCalls)
	}
	readiness := &natsReadinessFake{err: errors.New("stream is not durably replicated")}
	if _, err := NewNATSConsumer(context.Background(), api, readiness, config); err == nil ||
		readiness.calls != 1 || api.infoCalls != 0 {
		t.Fatalf("failed readiness error=%v readiness=%#v infoCalls=%d", err, readiness, api.infoCalls)
	}

	desired := desiredNATSConsumerConfig(config)
	api = &natsConsumerAPIFake{info: &nats.ConsumerInfo{
		Stream: config.Stream, Name: config.Durable, Config: desired,
	}}
	readiness = &natsReadinessFake{}
	if _, err := NewNATSConsumer(context.Background(), api, readiness, config); err == nil ||
		readiness.calls != 1 || api.infoCalls != 1 || api.pullCalls != 1 {
		t.Fatalf("ready bind error=%v readiness=%#v api=%#v", err, readiness, api)
	}
	requirement := readiness.requirement
	if requirement.Stream != config.Stream || requirement.SourceSubject != PendingHandoffSubject ||
		requirement.QuarantineSubject != HandoffQuarantineSubject || requirement.Durable != PendingHandoffDurable ||
		requirement.RequiredStorage != nats.FileStorage || requirement.RequiredRetention != nats.LimitsPolicy ||
		requirement.RequiredDiscard != nats.DiscardOld || requirement.MinimumRetention != minimumHandoffRetention ||
		requirement.MinimumDuplicateWindow != minimumHandoffDedupe || requirement.MinimumReplicas != 1 ||
		requirement.MinimumStreamMaxMsgBytes != max(maximumPendingHandoffPayloadBytes, maximumQuarantineWireBytes)+maximumBrokerHeaderBytes ||
		requirement.RequiredMaxDeliver != unlimitedNATSDeliveries ||
		requirement.RequiredAckPolicy != nats.AckExplicitPolicy ||
		requirement.RequiredDeliverPolicy != nats.DeliverAllPolicy {
		t.Fatalf("readiness requirement = %#v", requirement)
	}
}

func TestEnsureNATSConsumerCreatesMissingAndRejectsPolicyDrift(t *testing.T) {
	config := DefaultNATSConsumerConfig()
	api := &natsConsumerAPIFake{infoErr: nats.ErrConsumerNotFound}
	if err := ensureNATSHandoffConsumer(context.Background(), api, config); err != nil {
		t.Fatal(err)
	}
	if api.infoCalls != 1 || api.addCalls != 1 || api.addedStream != config.Stream ||
		api.addedConfig == nil || api.addedConfig.Durable != PendingHandoffDurable ||
		api.addedConfig.FilterSubject != PendingHandoffSubject {
		t.Fatalf("consumer ensure calls info=%d add=%d stream=%q config=%#v", api.infoCalls, api.addCalls, api.addedStream, api.addedConfig)
	}

	desired := desiredNATSConsumerConfig(config)
	desired.MaxAckPending++
	drifted := &natsConsumerAPIFake{info: &nats.ConsumerInfo{
		Stream: config.Stream, Name: config.Durable, Config: desired,
	}}
	if err := ensureNATSHandoffConsumer(context.Background(), drifted, config); err == nil {
		t.Fatal("existing consumer policy drift was accepted")
	}

	for name, info := range map[string]*nats.ConsumerInfo{
		"wrong stream": {
			Stream: config.Stream + "_OTHER", Name: config.Durable,
			Config: desiredNATSConsumerConfig(config),
		},
		"wrong name": {
			Stream: config.Stream, Name: config.Durable + "-other",
			Config: desiredNATSConsumerConfig(config),
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := &natsConsumerAPIFake{info: info}
			if err := ensureNATSHandoffConsumer(context.Background(), api, config); err == nil {
				t.Fatal("consumer identity drift was accepted")
			}
		})
	}
}

func TestNATSConsumerConfigurationRejectsWidenedOrUnboundedPolicy(t *testing.T) {
	base := DefaultNATSConsumerConfig()
	tests := map[string]func(*NATSConsumerConfig){
		"different durable":     func(config *NATSConsumerConfig) { config.Durable = "another-consumer" },
		"unbounded pending":     func(config *NATSConsumerConfig) { config.MaxAckPending = 0 },
		"wide request":          func(config *NATSConsumerConfig) { config.MaxRequestBatch = config.MaxAckPending + 1 },
		"missing request bytes": func(config *NATSConsumerConfig) { config.MaxRequestMaxBytes = 0 },
		"narrow request bytes": func(config *NATSConsumerConfig) {
			config.MaxRequestMaxBytes = config.MaxRequestBatch*(maximumPendingHandoffPayloadBytes+maximumBrokerHeaderBytes) - 1
		},
		"wide request bytes": func(config *NATSConsumerConfig) {
			config.MaxRequestMaxBytes = maximumConsumerRequestMaxBytes + 1
		},
		"wide ack confirm": func(config *NATSConsumerConfig) {
			config.AckConfirmWait = maximumConsumerAckConfirmWait + time.Nanosecond
		},
		"short ack timeout": func(config *NATSConsumerConfig) { config.AckBackOff[0] = config.AckWait - time.Second },
		"decreasing backoff": func(config *NATSConsumerConfig) {
			config.AckBackOff = []time.Duration{time.Minute, time.Second}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := cloneNATSConsumerConfig(base)
			mutate(&config)
			if err := validateNATSConsumerConfig(config); err == nil {
				t.Fatal("invalid NATS consumer policy was accepted")
			}
		})
	}
}

func TestNewNATSConsumerFailsClosedWhenBindReturnsNoSubscription(t *testing.T) {
	config := DefaultNATSConsumerConfig()
	desired := desiredNATSConsumerConfig(config)
	api := &natsConsumerAPIFake{info: &nats.ConsumerInfo{
		Stream: config.Stream, Name: config.Durable, Config: desired,
	}}
	if _, err := NewNATSConsumer(context.Background(), api, &natsReadinessFake{}, config); err == nil {
		t.Fatal("nil pull subscription was accepted")
	}
	if api.pullCalls != 1 {
		t.Fatalf("PullSubscribe calls = %d", api.pullCalls)
	}
}

func TestNATSDeliveryPreservesDuplicateEventTypeHeaders(t *testing.T) {
	message := nats.NewMsg(PendingHandoffSubject)
	message.Header.Add(handoffEventTypeHeader, PendingHandoffEventType)
	message.Header.Add(handoffEventTypeHeader, PendingHandoffEventType)
	delivery := &natsDelivery{message: message, attempt: 2, streamSequence: 3}
	if eventTypes := delivery.EventTypes(); len(eventTypes) != 2 ||
		eventTypes[0] != PendingHandoffEventType || eventTypes[1] != PendingHandoffEventType {
		t.Fatalf("EventTypes() = %v", eventTypes)
	}
	if _, err := decodePendingHandoff(delivery); err == nil {
		t.Fatal("duplicate event type headers were collapsed")
	}
}

func TestNATSDeliveryPreservesAndRejectsDuplicateMessageIDHeaders(t *testing.T) {
	record := testRecord(t)
	pending := pendingDelivery(record.HandoffID)
	message := nats.NewMsg(PendingHandoffSubject)
	message.Data = pending.payload
	message.Header.Set(handoffEventTypeHeader, PendingHandoffEventType)
	message.Header.Add(nats.MsgIdHdr, pending.messageID)
	message.Header.Add(nats.MsgIdHdr, pending.messageID)
	delivery := &natsDelivery{message: message, attempt: 1, streamSequence: 2}
	if len(delivery.MessageIDs()) != 2 {
		t.Fatalf("MessageIDs() = %v", delivery.MessageIDs())
	}
	if _, err := decodePendingHandoff(delivery); err == nil {
		t.Fatal("duplicate Nats-Msg-Id headers were accepted")
	}
}

func TestNATSQuarantineSinkPublishesClosedDurableRecord(t *testing.T) {
	record := testRecord(t)
	delivery := pendingDelivery(record.HandoffID)
	worker := newWorkerForTest(t, &workerServiceFake{}, &workerQuarantineFake{})
	terminal := worker.quarantineRecord(delivery, record.HandoffID, "handoff_conflict", ErrConflict)
	publisher := &natsPublisherFake{}
	sink, err := NewNATSQuarantineSink(publisher, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Quarantine(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 1 || publisher.message == nil ||
		publisher.message.Subject != HandoffQuarantineSubject ||
		publisher.message.Header.Get(handoffEventTypeHeader) != HandoffQuarantineEventType ||
		publisher.message.Header.Get(handoffSourceIDHeader) != delivery.messageID ||
		publisher.message.Header.Get(nats.MsgIdHdr) != quarantineMessageID(terminal) {
		t.Fatalf("published quarantine message = %#v", publisher.message)
	}
	var decoded QuarantineRecord
	decoder := json.NewDecoder(bytes.NewReader(publisher.message.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Reason != terminal.Reason || decoded.SourcePayloadHash != terminal.SourcePayloadHash ||
		decoded.HandoffID == nil || *decoded.HandoffID != record.HandoffID.String() {
		t.Fatalf("quarantine wire = %#v", decoded)
	}

	changedObservation := terminal
	changedObservation.Detail = "another observation of the same source message"
	changedObservation.QuarantinedAt = "2026-07-20T13:00:00Z"
	if quarantineMessageID(changedObservation) != quarantineMessageID(terminal) {
		t.Fatal("same source delivery produced a different quarantine dedupe ID")
	}
}

func TestNATSQuarantineSinkRefusesCorruptRecordAndPublishFailure(t *testing.T) {
	record := testRecord(t)
	delivery := pendingDelivery(record.HandoffID)
	worker := newWorkerForTest(t, &workerServiceFake{}, &workerQuarantineFake{})
	terminal := worker.quarantineRecord(delivery, record.HandoffID, "handoff_conflict", ErrConflict)
	publisher := &natsPublisherFake{}
	sink, err := NewNATSQuarantineSink(publisher, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := terminal
	corrupt.SourcePayloadHash = "sha256:" + strings.Repeat("0", 64)
	if err := sink.Quarantine(context.Background(), corrupt); err == nil || publisher.calls != 0 {
		t.Fatalf("corrupt quarantine result err=%v calls=%d", err, publisher.calls)
	}
	publisher.err = errors.New("JetStream unavailable")
	if err := sink.Quarantine(context.Background(), terminal); err == nil || publisher.calls != 1 {
		t.Fatalf("publish failure err=%v calls=%d", err, publisher.calls)
	}
}

func TestNATSQuarantineSinkRejectsUnboundedPublishWait(t *testing.T) {
	if _, err := NewNATSQuarantineSink(
		&natsPublisherFake{}, maximumQuarantinePublishWait+time.Nanosecond,
	); err == nil {
		t.Fatal("unbounded quarantine publish timeout was accepted")
	}
}

func TestQuarantineObservationBoundsUntrustedEnvelopeMembers(t *testing.T) {
	record := testRecord(t)
	delivery := pendingDelivery(record.HandoffID)
	delivery.messageID = strings.Repeat("m", maximumTerminalMessageIDBytes+10)
	delivery.subject = strings.Repeat("s", maximumTerminalSubjectBytes+10)
	delivery.payload = []byte(strings.Repeat("p", maximumTerminalPayloadBytes+10))
	delivery.eventTypes = make([]string, maximumTerminalEventTypeCount+1)
	for index := range delivery.eventTypes {
		delivery.eventTypes[index] = strings.Repeat("e", maximumTerminalEventTypeBytes+10)
	}
	worker := newWorkerForTest(t, &workerServiceFake{}, &workerQuarantineFake{})
	terminal := worker.quarantineRecord(delivery, record.HandoffID, "invalid_message", ErrInvalid)
	if len(terminal.SourceMessageID) != maximumTerminalMessageIDBytes || !terminal.SourceMessageIDTruncated ||
		len(terminal.SourceSubject) != maximumTerminalSubjectBytes || !terminal.SourceSubjectTruncated ||
		len(terminal.SourceEventTypes) != maximumTerminalEventTypeCount || !terminal.SourceEventTypesTruncated ||
		terminal.SourcePayloadSize != maximumTerminalPayloadBytes+10 || !terminal.SourcePayloadTruncated {
		t.Fatalf("bounded quarantine record = %#v", terminal)
	}
	if err := validateQuarantineRecord(terminal); err != nil {
		t.Fatal(err)
	}
}
