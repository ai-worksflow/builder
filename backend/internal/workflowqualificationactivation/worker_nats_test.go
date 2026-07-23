package workflowqualificationactivation

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
	info        *nats.ConsumerInfo
	infoErr     error
	addedConfig *nats.ConsumerConfig
	addCalls    int
	pullCalls   int
	infoCalls   int
}

func (api *natsConsumerAPIFake) ConsumerInfo(string, string, ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	api.infoCalls++
	return api.info, api.infoErr
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

func (api *natsConsumerAPIFake) AddConsumer(stream string, config *nats.ConsumerConfig, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	api.addCalls++
	copy := *config
	copy.BackOff = slices.Clone(config.BackOff)
	api.addedConfig = &copy
	return &nats.ConsumerInfo{Stream: stream, Name: config.Durable, Config: *config}, nil
}

func (api *natsConsumerAPIFake) PullSubscribe(string, string, ...nats.SubOpt) (*nats.Subscription, error) {
	api.pullCalls++
	return nil, errors.New("test bind stop")
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
	return &nats.PubAck{Stream: DefaultActivationEventStream, Sequence: 1}, nil
}

func TestDefaultNATSConsumerConfigIsFrozenBoundedManualAck(t *testing.T) {
	config := DefaultNATSConsumerConfig()
	if err := validateNATSConsumerConfig(config); err != nil {
		t.Fatal(err)
	}
	desired := desiredNATSConsumerConfig(config)
	if desired.Durable != ActivationDurable || desired.FilterSubject != WorkflowRunEventSubject ||
		desired.AckPolicy != nats.AckExplicitPolicy || desired.DeliverPolicy != nats.DeliverAllPolicy ||
		desired.DeliverSubject != "" || desired.MaxDeliver != unlimitedNATSDeliveries || desired.MaxAckPending != 8 ||
		desired.MaxRequestBatch != 8 || desired.MaxRequestMaxBytes != config.MaxRequestMaxBytes || len(desired.BackOff) == 0 {
		t.Fatalf("default durable consumer = %#v", desired)
	}
	config.AckBackOff[0] = time.Millisecond
	if desired.BackOff[0] == time.Millisecond {
		t.Fatal("desired consumer retained mutable caller backoff")
	}
}

func TestNewNATSConsumerRequiresStreamPostureBeforeConsumerAccess(t *testing.T) {
	config := DefaultNATSConsumerConfig()
	api := &natsConsumerAPIFake{}
	if _, err := NewNATSConsumer(context.Background(), api, nil, config); err == nil || api.infoCalls != 0 {
		t.Fatalf("nil readiness error=%v infoCalls=%d", err, api.infoCalls)
	}
	readiness := &natsReadinessFake{err: errors.New("stream is not replicated")}
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
	if requirement.Stream != config.Stream || requirement.SourceSubject != WorkflowRunEventSubject ||
		requirement.QuarantineSubject != ActivationQuarantineSubject || requirement.Durable != ActivationDurable ||
		requirement.RequiredStorage != nats.FileStorage || requirement.RequiredRetention != nats.LimitsPolicy ||
		requirement.RequiredDiscard != nats.DiscardOld ||
		requirement.MinimumRetention != minimumActivationRetention ||
		requirement.MinimumDuplicateWindow != minimumActivationDedupe ||
		requirement.MinimumReplicas != 1 ||
		requirement.MinimumStreamMaxMsgBytes != maximumNodeCompletedPayloadBytes+maximumBrokerHeaderBytes ||
		requirement.RequiredMaxDeliver != unlimitedNATSDeliveries ||
		requirement.RequiredAckPolicy != nats.AckExplicitPolicy ||
		requirement.RequiredDeliverPolicy != nats.DeliverAllPolicy {
		t.Fatalf("readiness requirement = %#v", requirement)
	}
}

func TestEnsureNATSConsumerCreatesMissingAndRejectsPolicyDrift(t *testing.T) {
	config := DefaultNATSConsumerConfig()
	api := &natsConsumerAPIFake{infoErr: nats.ErrConsumerNotFound}
	if err := ensureNATSActivationConsumer(context.Background(), api, config); err != nil {
		t.Fatal(err)
	}
	if api.addCalls != 1 || api.addedConfig == nil || api.addedConfig.Durable != ActivationDurable ||
		api.addedConfig.FilterSubject != WorkflowRunEventSubject {
		t.Fatalf("created consumer = %#v", api.addedConfig)
	}

	desired := desiredNATSConsumerConfig(config)
	desired.MaxAckPending++
	drifted := &natsConsumerAPIFake{info: &nats.ConsumerInfo{
		Stream: config.Stream, Name: config.Durable, Config: desired,
	}}
	if err := ensureNATSActivationConsumer(context.Background(), drifted, config); err == nil {
		t.Fatal("existing consumer policy drift was accepted")
	}
}

func TestNATSConsumerReadinessRechecksStreamAndExactDurablePolicy(t *testing.T) {
	config := DefaultNATSConsumerConfig()
	desired := desiredNATSConsumerConfig(config)
	api := &natsConsumerAPIFake{info: &nats.ConsumerInfo{
		Stream: config.Stream, Name: config.Durable, Config: desired,
	}}
	stream := &natsReadinessFake{}
	consumer := &NATSConsumer{
		subscription: &nats.Subscription{},
		config:       cloneNATSConsumerConfig(config),
		jetStream:    api,
		readiness:    stream,
	}
	if err := consumer.Readiness(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stream.calls != 1 || api.infoCalls != 1 {
		t.Fatalf("readiness calls stream=%d consumer=%d, want 1/1", stream.calls, api.infoCalls)
	}

	drifted := desired
	drifted.MaxAckPending++
	api.info = &nats.ConsumerInfo{Stream: config.Stream, Name: config.Durable, Config: drifted}
	if err := consumer.Readiness(context.Background()); err == nil {
		t.Fatal("durable consumer policy drift was accepted by readiness")
	}

	api.info = &nats.ConsumerInfo{Stream: config.Stream, Name: config.Durable, Config: desired}
	consumer.closed.Store(true)
	beforeStream, beforeConsumer := stream.calls, api.infoCalls
	if err := consumer.Readiness(context.Background()); err == nil ||
		stream.calls != beforeStream || api.infoCalls != beforeConsumer {
		t.Fatalf("closed consumer readiness error=%v calls=%d/%d", err, stream.calls, api.infoCalls)
	}
}

func TestNATSConsumerConfigurationRejectsUnboundedOrOverflowingPolicy(t *testing.T) {
	base := DefaultNATSConsumerConfig()
	tests := map[string]func(*NATSConsumerConfig){
		"wide stream name": func(config *NATSConsumerConfig) {
			config.Stream = strings.Repeat("s", maximumNATSStreamNameBytes+1)
		},
		"wide ack wait": func(config *NATSConsumerConfig) {
			config.AckWait = maximumConsumerAckWait + time.Nanosecond
		},
		"wide ack pending": func(config *NATSConsumerConfig) {
			config.MaxAckPending = maximumNATSMaxAckPending + 1
		},
		"wide request batch": func(config *NATSConsumerConfig) {
			config.MaxRequestBatch = maximumConsumerRequestBatch + 1
		},
		"missing request bytes": func(config *NATSConsumerConfig) { config.MaxRequestMaxBytes = 0 },
		"wide request bytes": func(config *NATSConsumerConfig) {
			config.MaxRequestMaxBytes = maximumConsumerRequestMaxBytes + 1
		},
		"narrow request bytes": func(config *NATSConsumerConfig) {
			config.MaxRequestMaxBytes = config.MaxRequestBatch*(maximumNodeCompletedPayloadBytes+maximumBrokerHeaderBytes) - 1
		},
		"wide request expiry": func(config *NATSConsumerConfig) {
			config.MaxRequestExpires = maximumNATSRequestExpires + time.Nanosecond
		},
		"wide fetch wait": func(config *NATSConsumerConfig) {
			config.FetchWait = maximumNATSFetchWait + time.Nanosecond
			config.MaxRequestExpires = config.FetchWait
		},
		"wide waiting pulls": func(config *NATSConsumerConfig) {
			config.MaxWaiting = maximumNATSMaxWaiting + 1
		},
		"wide ack confirm": func(config *NATSConsumerConfig) {
			config.AckConfirmWait = maximumConsumerAckConfirmWait + time.Nanosecond
		},
		"wide backoff": func(config *NATSConsumerConfig) {
			config.AckBackOff = []time.Duration{maximumNATSAckBackOff + time.Nanosecond}
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

func TestNATSDeliveryPreservesDuplicateEventTypeHeaders(t *testing.T) {
	message := nats.NewMsg(WorkflowRunEventSubject)
	message.Header.Add(workflowEventTypeHeader, NodeCompletedEventType)
	message.Header.Add(workflowEventTypeHeader, NodeCompletedEventType)
	delivery := &natsDelivery{message: message, attempt: 1, streamSequence: 2}
	if len(delivery.EventTypes()) != 2 {
		t.Fatalf("EventTypes() = %v", delivery.EventTypes())
	}
	if _, _, err := decodeNodeCompletedDelivery(delivery); err == nil {
		t.Fatal("duplicate event type headers were collapsed")
	}
}

func TestNATSDeliveryPreservesAndRejectsDuplicateMessageIDHeaders(t *testing.T) {
	delivery := nodeCompletedDelivery(t)
	message := nats.NewMsg(WorkflowRunEventSubject)
	message.Data = delivery.payload
	message.Header.Set(workflowEventTypeHeader, NodeCompletedEventType)
	message.Header.Add(nats.MsgIdHdr, testCompletionEventID)
	message.Header.Add(nats.MsgIdHdr, testCompletionEventID)
	natsDelivery := &natsDelivery{message: message, attempt: 1, streamSequence: 2}
	if len(natsDelivery.MessageIDs()) != 2 {
		t.Fatalf("MessageIDs() = %v", natsDelivery.MessageIDs())
	}
	if _, _, err := decodeNodeCompletedDelivery(natsDelivery); err == nil {
		t.Fatal("duplicate Nats-Msg-Id headers were accepted")
	}
}

func TestNATSQuarantineSinkPublishesDeterministicClosedRecord(t *testing.T) {
	id, _ := ParseCompletionEventID(testCompletionEventID)
	delivery := nodeCompletedDelivery(t)
	worker := newWorkerForTest(t, &workerServiceFake{}, &workerQuarantineFake{})
	record := worker.quarantineRecord(delivery, id, "activation_conflict", ErrConflict)
	publisher := &natsPublisherFake{}
	sink, err := NewNATSQuarantineSink(publisher, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Quarantine(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 1 || publisher.message == nil ||
		publisher.message.Subject != ActivationQuarantineSubject ||
		publisher.message.Header.Get(workflowEventTypeHeader) != ActivationQuarantineEventType ||
		publisher.message.Header.Get(activationSourceIDHeader) != delivery.messageID ||
		publisher.message.Header.Get(nats.MsgIdHdr) != quarantineMessageID(record) {
		t.Fatalf("published quarantine message = %#v", publisher.message)
	}
	var decoded QuarantineRecord
	decoder := json.NewDecoder(bytes.NewReader(publisher.message.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CompletionEventID == nil || *decoded.CompletionEventID != id.String() ||
		decoded.SourcePayloadHash != record.SourcePayloadHash || decoded.Reason != record.Reason {
		t.Fatalf("quarantine wire = %#v", decoded)
	}
	changed := record
	changed.Detail = "another observation"
	changed.QuarantinedAt = "2026-07-20T06:00:00Z"
	if quarantineMessageID(changed) != quarantineMessageID(record) {
		t.Fatal("same source delivery produced a new quarantine dedupe ID")
	}
}

func TestNATSQuarantineSinkRefusesCorruptRecord(t *testing.T) {
	id, _ := ParseCompletionEventID(testCompletionEventID)
	delivery := nodeCompletedDelivery(t)
	worker := newWorkerForTest(t, &workerServiceFake{}, &workerQuarantineFake{})
	record := worker.quarantineRecord(delivery, id, "activation_conflict", ErrConflict)
	record.SourcePayloadHash = "sha256:" + string(make([]byte, 64))
	publisher := &natsPublisherFake{}
	sink, err := NewNATSQuarantineSink(publisher, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Quarantine(context.Background(), record); err == nil || publisher.calls != 0 {
		t.Fatalf("corrupt record publish err=%v calls=%d", err, publisher.calls)
	}
}

func TestNATSQuarantineSinkRejectsUnboundedPublishWait(t *testing.T) {
	if _, err := NewNATSQuarantineSink(
		&natsPublisherFake{}, maximumQuarantinePublishWait+time.Nanosecond,
	); err == nil {
		t.Fatal("unbounded quarantine publish timeout was accepted")
	}
}
