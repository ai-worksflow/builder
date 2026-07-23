package workflowqualificationactivation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

type workerServiceFake struct {
	record        Record
	activateErr   error
	inspectErr    error
	activateCalls int
	inspectCalls  int
	activateID    CompletionEventID
	inspectID     CompletionEventID
}

func (service *workerServiceFake) Activate(_ context.Context, id CompletionEventID) (Record, error) {
	service.activateCalls++
	service.activateID = id
	return service.record, service.activateErr
}

func (service *workerServiceFake) Inspect(_ context.Context, id CompletionEventID) (Record, error) {
	service.inspectCalls++
	service.inspectID = id
	return service.record, service.inspectErr
}

type workerConsumerFake struct {
	limits ConsumerLimits
}

func (consumer *workerConsumerFake) Fetch(context.Context, int) ([]Delivery, error) { return nil, nil }
func (consumer *workerConsumerFake) Limits() ConsumerLimits                         { return consumer.limits }
func (consumer *workerConsumerFake) Close() error                                   { return nil }

type workerDeliveryFake struct {
	subject        string
	eventTypes     []string
	payload        []byte
	messageID      string
	messageIDs     []string
	streamSequence uint64
	attempt        uint64
	acks           int
	naks           int
	nakDelays      []time.Duration
	terms          int
}

func (delivery *workerDeliveryFake) Subject() string { return delivery.subject }
func (delivery *workerDeliveryFake) EventTypes() []string {
	return append([]string(nil), delivery.eventTypes...)
}
func (delivery *workerDeliveryFake) Payload() []byte   { return delivery.payload }
func (delivery *workerDeliveryFake) MessageID() string { return delivery.messageID }
func (delivery *workerDeliveryFake) MessageIDs() []string {
	return append([]string(nil), delivery.messageIDs...)
}
func (delivery *workerDeliveryFake) StreamSequence() uint64 { return delivery.streamSequence }
func (delivery *workerDeliveryFake) Attempt() uint64        { return delivery.attempt }
func (delivery *workerDeliveryFake) Ack(context.Context) error {
	delivery.acks++
	return nil
}
func (delivery *workerDeliveryFake) Nak(_ context.Context, delay time.Duration) error {
	delivery.naks++
	delivery.nakDelays = append(delivery.nakDelays, delay)
	return nil
}
func (delivery *workerDeliveryFake) Term(context.Context) error {
	delivery.terms++
	return nil
}

type workerQuarantineFake struct {
	records []QuarantineRecord
	err     error
}

func (sink *workerQuarantineFake) Quarantine(_ context.Context, record QuarantineRecord) error {
	sink.records = append(sink.records, record)
	return sink.err
}

func newWorkerForTest(t *testing.T, service ActivationService, quarantine QuarantineSink) *Worker {
	t.Helper()
	config := DefaultWorkerConfig()
	ackConfirmWait := time.Second
	requiredAckWait, err := requiredConsumerAckWait(config, ackConfirmWait)
	if err != nil {
		t.Fatal(err)
	}
	consumer := &workerConsumerFake{limits: ConsumerLimits{
		MaxDeliver: -1, MaxAckPending: config.MaxInFlight, AckWait: requiredAckWait,
		AckConfirmWait: ackConfirmWait, MaxRequestBatch: config.MaxInFlight,
		MaxRequestMaxBytes: config.MaxInFlight * (maximumNodeCompletedPayloadBytes + maximumBrokerHeaderBytes),
	}}
	worker, err := NewWorker(service, consumer, quarantine, config)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return time.Date(2026, 7, 20, 5, 0, 0, 0, time.UTC) }
	return worker
}

func TestNewWorkerRequiresUnlimitedBrokerAndFullBoundedFetchWindow(t *testing.T) {
	config := DefaultWorkerConfig()
	ackConfirmWait := time.Second
	requiredAckWait, err := requiredConsumerAckWait(config, ackConfirmWait)
	if err != nil {
		t.Fatal(err)
	}
	base := ConsumerLimits{
		MaxDeliver: -1, MaxAckPending: config.MaxInFlight, AckWait: requiredAckWait,
		AckConfirmWait: ackConfirmWait, MaxRequestBatch: config.MaxInFlight,
		MaxRequestMaxBytes: config.MaxInFlight * (maximumNodeCompletedPayloadBytes + maximumBrokerHeaderBytes),
	}
	newWorker := func(limits ConsumerLimits) error {
		_, err := NewWorker(
			&workerServiceFake{}, &workerConsumerFake{limits: limits},
			&workerQuarantineFake{}, config,
		)
		return err
	}
	finite := base
	finite.MaxDeliver = 20
	if err := newWorker(finite); err == nil {
		t.Fatal("finite broker MaxDeliver was accepted")
	}
	shortBatch := base
	shortBatch.MaxRequestBatch = config.MaxInFlight - 1
	if err := newWorker(shortBatch); err == nil {
		t.Fatal("consumer MaxRequestBatch narrower than MaxInFlight was accepted")
	}
	shortBytes := base
	shortBytes.MaxRequestMaxBytes--
	if err := newWorker(shortBytes); err == nil {
		t.Fatal("consumer MaxRequestMaxBytes narrower than bounded batch was accepted")
	}
	shortAck := base
	shortAck.AckWait--
	if err := newWorker(shortAck); err == nil {
		t.Fatal("AckWait omitted part of activate/reconcile/inspect/terminal/ack window")
	}
	wideAck := base
	wideAck.AckWait = maximumConsumerAckWait + time.Nanosecond
	if err := newWorker(wideAck); err == nil {
		t.Fatal("unbounded consumer AckWait was accepted")
	}
	widePending := base
	widePending.MaxAckPending = maximumConsumerAckPending + 1
	if err := newWorker(widePending); err == nil {
		t.Fatal("unbounded consumer MaxAckPending was accepted")
	}
	wideBatch := base
	wideBatch.MaxRequestBatch = maximumConsumerRequestBatch + 1
	if err := newWorker(wideBatch); err == nil {
		t.Fatal("unbounded consumer MaxRequestBatch was accepted")
	}
	if err := newWorker(base); err != nil {
		t.Fatalf("exact bounded consumer contract was rejected: %v", err)
	}
}

func TestRequiredConsumerAckWaitIncludesCommitUnknownAndRejectsOverflow(t *testing.T) {
	config := DefaultWorkerConfig()
	ackConfirmWait := 2 * time.Second
	got, err := requiredConsumerAckWait(config, ackConfirmWait)
	want := 2*config.OperationTimeout + commitUnknownInspectTimeout + config.TerminalTimeout + ackConfirmWait
	if err != nil || got != want {
		t.Fatalf("requiredConsumerAckWait() = %s, %v; want %s", got, err, want)
	}
	overflow := config
	overflow.OperationTimeout = time.Duration(1<<63 - 1)
	if _, err := requiredConsumerAckWait(overflow, ackConfirmWait); err == nil {
		t.Fatal("AckWait duration overflow was accepted")
	}
}

func nodeCompletedDelivery(t *testing.T) *workerDeliveryFake {
	t.Helper()
	payload := map[string]any{
		"id":         testCompletionEventID,
		"projectId":  "20202020-2020-4020-8020-202020202020",
		"runId":      "30303030-3030-4030-8030-303030303030",
		"sequence":   7,
		"type":       NodeCompletedEventType,
		"occurredAt": "2026-07-20T05:00:00.000000Z",
		"payload":    map[string]any{"attempt": 1},
		"nodeKey":    "release-quality",
		"actorId":    "40404040-4040-4040-8040-404040404040",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &workerDeliveryFake{
		subject:    WorkflowRunEventSubject,
		eventTypes: []string{NodeCompletedEventType},
		payload:    encoded, messageID: testCompletionEventID, messageIDs: []string{testCompletionEventID},
		streamSequence: 11, attempt: 1,
	}
}

func TestWorkerSuppliesOnlyExactCompletionEventIDAndAcksNonTarget(t *testing.T) {
	id, _ := ParseCompletionEventID(testCompletionEventID)
	service := &workerServiceFake{record: ignoredRecord(id)}
	delivery := nodeCompletedDelivery(t)
	worker := newWorkerForTest(t, service, &workerQuarantineFake{})
	if err := worker.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if service.activateCalls != 1 || service.activateID != id || service.inspectCalls != 0 ||
		delivery.acks != 1 || delivery.naks != 0 || delivery.terms != 0 {
		t.Fatalf("service=%#v delivery=%#v", service, delivery)
	}
}

func TestWorkerAcksExactOtherWorkflowEventTypesWithoutResolvingFacts(t *testing.T) {
	service := &workerServiceFake{}
	delivery := nodeCompletedDelivery(t)
	delivery.eventTypes = []string{"node.started"}
	var envelope map[string]any
	if err := json.Unmarshal(delivery.payload, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["type"] = "node.started"
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	delivery.payload = encoded
	worker := newWorkerForTest(t, service, &workerQuarantineFake{})
	if err := worker.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.acks != 1 || service.activateCalls != 0 || service.inspectCalls != 0 {
		t.Fatalf("non-target header was not passively ACKed: service=%#v delivery=%#v", service, delivery)
	}
}

func TestWorkerQuarantinesMalformedOrAmbiguousNodeCompletedEnvelope(t *testing.T) {
	tests := map[string]func(*workerDeliveryFake){
		"malformed non-node event": func(delivery *workerDeliveryFake) {
			delivery.eventTypes = []string{"node.started"}
			delivery.payload = []byte(`{"type":"node.started"}`)
		},
		"duplicate event header": func(delivery *workerDeliveryFake) {
			delivery.eventTypes = append(delivery.eventTypes, NodeCompletedEventType)
		},
		"duplicate message ID header": func(delivery *workerDeliveryFake) {
			delivery.messageIDs = append(delivery.messageIDs, testCompletionEventID)
		},
		"different message ID": func(delivery *workerDeliveryFake) { delivery.messageID = "different" },
		"different message ID values": func(delivery *workerDeliveryFake) {
			delivery.messageIDs = []string{"50505050-5050-4050-8050-505050505050"}
		},
		"header payload type mismatch": func(delivery *workerDeliveryFake) {
			delivery.eventTypes = []string{"node.started"}
		},
		"duplicate JSON ID": func(delivery *workerDeliveryFake) {
			delivery.payload = []byte(fmt.Sprintf(`{"id":%q,"id":%q}`, testCompletionEventID, testCompletionEventID))
		},
		"unknown member": func(delivery *workerDeliveryFake) {
			delivery.payload = append(delivery.payload[:len(delivery.payload)-1], []byte(`,"authorityId":"forged"}`)...)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			service := &workerServiceFake{}
			delivery := nodeCompletedDelivery(t)
			mutate(delivery)
			quarantine := &workerQuarantineFake{}
			worker := newWorkerForTest(t, service, quarantine)
			if err := worker.Handle(context.Background(), delivery); err != nil {
				t.Fatal(err)
			}
			if service.activateCalls != 0 || delivery.acks != 0 || delivery.naks != 0 ||
				delivery.terms != 1 || len(quarantine.records) != 1 || quarantine.records[0].Reason != "invalid_message" {
				t.Fatalf("service=%#v delivery=%#v quarantine=%#v", service, delivery, quarantine.records)
			}
		})
	}
}

func TestWorkerReconcilesUnknownUsingSameEventID(t *testing.T) {
	id, _ := ParseCompletionEventID(testCompletionEventID)
	service := &workerServiceFake{record: ignoredRecord(id), activateErr: ErrOutcomeUnknown}
	delivery := nodeCompletedDelivery(t)
	worker := newWorkerForTest(t, service, &workerQuarantineFake{})
	if err := worker.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if service.activateCalls != 1 || service.inspectCalls != 1 || service.activateID != id || service.inspectID != id ||
		delivery.acks != 1 || delivery.naks != 0 || delivery.terms != 0 {
		t.Fatalf("service=%#v delivery=%#v", service, delivery)
	}
}

func TestWorkerRetriesTransientAndQuarantinesBeforeDeliveryCeiling(t *testing.T) {
	service := &workerServiceFake{activateErr: ErrRetryable}
	delivery := nodeCompletedDelivery(t)
	worker := newWorkerForTest(t, service, &workerQuarantineFake{})
	if err := worker.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.naks != 1 || len(delivery.nakDelays) != 1 || delivery.acks != 0 || delivery.terms != 0 {
		t.Fatalf("retry delivery = %#v", delivery)
	}

	delivery = nodeCompletedDelivery(t)
	delivery.attempt = uint64(worker.config.TerminalAttemptThreshold)
	quarantine := &workerQuarantineFake{}
	worker = newWorkerForTest(t, service, quarantine)
	if err := worker.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.terms != 1 || len(quarantine.records) != 1 ||
		quarantine.records[0].Reason != "retry_exhausted_retryable_contention" {
		t.Fatalf("terminal delivery=%#v quarantine=%#v", delivery, quarantine.records)
	}
}

func TestWorkerDoesNotTermWhenDurableQuarantineFails(t *testing.T) {
	service := &workerServiceFake{activateErr: ErrRetryable}
	delivery := nodeCompletedDelivery(t)
	quarantine := &workerQuarantineFake{err: errors.New("quarantine unavailable")}
	worker := newWorkerForTest(t, service, quarantine)
	delivery.attempt = uint64(worker.config.TerminalAttemptThreshold + 100)
	if err := worker.Handle(context.Background(), delivery); err == nil {
		t.Fatal("quarantine failure was hidden")
	}
	if delivery.terms != 0 || delivery.naks != 1 || len(quarantine.records) != 1 {
		t.Fatalf("delivery=%#v quarantine=%#v", delivery, quarantine.records)
	}
}
