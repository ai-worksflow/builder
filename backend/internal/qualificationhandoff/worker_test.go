package qualificationhandoff

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type workerServiceFake struct {
	mu sync.Mutex

	completeRecord Record
	completeErr    error
	inspectRecord  Record
	inspectErr     error
	completeIDs    []uuid.UUID
	inspectIDs     []uuid.UUID
	complete       func(context.Context, uuid.UUID) (Record, error)
	inspect        func(context.Context, uuid.UUID) (Record, error)
}

func (service *workerServiceFake) Complete(ctx context.Context, handoffID uuid.UUID) (Record, error) {
	service.mu.Lock()
	service.completeIDs = append(service.completeIDs, handoffID)
	callback := service.complete
	record, err := service.completeRecord, service.completeErr
	service.mu.Unlock()
	if callback != nil {
		return callback(ctx, handoffID)
	}
	return cloneRecord(record), err
}

func (service *workerServiceFake) Inspect(ctx context.Context, handoffID uuid.UUID) (Record, error) {
	service.mu.Lock()
	service.inspectIDs = append(service.inspectIDs, handoffID)
	callback := service.inspect
	record, err := service.inspectRecord, service.inspectErr
	service.mu.Unlock()
	if callback != nil {
		return callback(ctx, handoffID)
	}
	return cloneRecord(record), err
}

type workerDeliveryFake struct {
	mu sync.Mutex

	subject        string
	eventTypes     []string
	payload        []byte
	messageID      string
	messageIDs     []string
	streamSequence uint64
	attempt        uint64
	ackErr         error
	nakErr         error
	termErr        error
	acks           int
	naks           int
	terms          int
	nakDelays      []time.Duration
}

func (delivery *workerDeliveryFake) Subject() string { return delivery.subject }
func (delivery *workerDeliveryFake) EventTypes() []string {
	return append([]string(nil), delivery.eventTypes...)
}
func (delivery *workerDeliveryFake) Payload() []byte {
	return append([]byte(nil), delivery.payload...)
}
func (delivery *workerDeliveryFake) MessageID() string { return delivery.messageID }
func (delivery *workerDeliveryFake) MessageIDs() []string {
	return append([]string(nil), delivery.messageIDs...)
}
func (delivery *workerDeliveryFake) StreamSequence() uint64 { return delivery.streamSequence }
func (delivery *workerDeliveryFake) Attempt() uint64        { return delivery.attempt }
func (delivery *workerDeliveryFake) Ack(ctx context.Context) error {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	delivery.acks++
	return delivery.ackErr
}
func (delivery *workerDeliveryFake) Nak(ctx context.Context, delay time.Duration) error {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	delivery.naks++
	delivery.nakDelays = append(delivery.nakDelays, delay)
	return delivery.nakErr
}
func (delivery *workerDeliveryFake) Term(ctx context.Context) error {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	delivery.terms++
	return delivery.termErr
}

type workerConsumerFake struct {
	mu         sync.Mutex
	deliveries []Delivery
	err        error
	limits     ConsumerLimits
	fetches    int
	maximum    int
	closed     int
}

func (consumer *workerConsumerFake) Fetch(_ context.Context, maximum int) ([]Delivery, error) {
	consumer.mu.Lock()
	defer consumer.mu.Unlock()
	consumer.fetches++
	consumer.maximum = maximum
	deliveries := append([]Delivery(nil), consumer.deliveries...)
	consumer.deliveries = nil
	return deliveries, consumer.err
}
func (consumer *workerConsumerFake) Limits() ConsumerLimits { return consumer.limits }
func (consumer *workerConsumerFake) Close() error {
	consumer.mu.Lock()
	defer consumer.mu.Unlock()
	consumer.closed++
	return nil
}

type workerQuarantineFake struct {
	mu      sync.Mutex
	records []QuarantineRecord
	err     error
	call    func(context.Context, QuarantineRecord) error
}

func (sink *workerQuarantineFake) Quarantine(ctx context.Context, record QuarantineRecord) error {
	sink.mu.Lock()
	sink.records = append(sink.records, record)
	callback, err := sink.call, sink.err
	sink.mu.Unlock()
	if callback != nil {
		return callback(ctx, record)
	}
	return err
}

func newWorkerForTest(t *testing.T, service CompletionService, sink QuarantineSink) *Worker {
	t.Helper()
	config := DefaultWorkerConfig()
	ackConfirmWait := time.Second
	requiredAckWait, err := requiredConsumerAckWait(config, ackConfirmWait)
	if err != nil {
		t.Fatal(err)
	}
	consumer := &workerConsumerFake{limits: ConsumerLimits{
		MaxDeliver: -1, MaxAckPending: config.MaxInFlight,
		AckWait: requiredAckWait, AckConfirmWait: ackConfirmWait,
		MaxRequestBatch:    config.MaxInFlight,
		MaxRequestMaxBytes: config.MaxInFlight * (maximumPendingHandoffPayloadBytes + maximumBrokerHeaderBytes),
	}}
	worker, err := NewWorker(service, consumer, sink, config)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time {
		return time.Date(2026, 7, 20, 12, 0, 0, 123000000, time.UTC)
	}
	return worker
}

func pendingDelivery(handoffID uuid.UUID) *workerDeliveryFake {
	return &workerDeliveryFake{
		subject: PendingHandoffSubject, eventTypes: []string{PendingHandoffEventType},
		payload:   []byte(fmt.Sprintf(`{"handoffId":%q}`, handoffID.String())),
		messageID: testUUID(31), messageIDs: []string{testUUID(31)},
		streamSequence: 41, attempt: 1,
	}
}

func TestDecodePendingHandoffRequiresClosedCanonicalEnvelope(t *testing.T) {
	handoffID := uuid.MustParse(testUUID(1))
	valid := pendingDelivery(handoffID)
	parsed, err := decodePendingHandoff(valid)
	if err != nil || parsed != handoffID {
		t.Fatalf("decodePendingHandoff() = %s, %v", parsed, err)
	}

	tests := map[string]func(*workerDeliveryFake){
		"wrong subject":      func(delivery *workerDeliveryFake) { delivery.subject += ".other" },
		"missing event type": func(delivery *workerDeliveryFake) { delivery.eventTypes = nil },
		"duplicate event type": func(delivery *workerDeliveryFake) {
			delivery.eventTypes = []string{PendingHandoffEventType, PendingHandoffEventType}
		},
		"wrong event type": func(delivery *workerDeliveryFake) { delivery.eventTypes[0] += ".other" },
		"missing message ID": func(delivery *workerDeliveryFake) {
			delivery.messageID = ""
			delivery.messageIDs = nil
		},
		"duplicate message ID": func(delivery *workerDeliveryFake) {
			delivery.messageIDs = append(delivery.messageIDs, delivery.messageID)
		},
		"different message ID view": func(delivery *workerDeliveryFake) {
			delivery.messageIDs[0] = testUUID(32)
		},
		"non canonical message ID": func(delivery *workerDeliveryFake) {
			delivery.messageID = "not-a-uuid"
			delivery.messageIDs = []string{"not-a-uuid"}
		},
		"unknown field": func(delivery *workerDeliveryFake) {
			delivery.payload = []byte(fmt.Sprintf(`{"handoffId":%q,"projectId":%q}`, handoffID, testUUID(2)))
		},
		"duplicate field": func(delivery *workerDeliveryFake) {
			delivery.payload = []byte(fmt.Sprintf(`{"handoffId":%q,"handoffId":%q}`, handoffID, handoffID))
		},
		"missing field": func(delivery *workerDeliveryFake) { delivery.payload = []byte(`{}`) },
		"null field":    func(delivery *workerDeliveryFake) { delivery.payload = []byte(`{"handoffId":null}`) },
		"non-string field": func(delivery *workerDeliveryFake) {
			delivery.payload = []byte(`{"handoffId":7}`)
		},
		"uppercase UUID": func(delivery *workerDeliveryFake) {
			delivery.payload = []byte(fmt.Sprintf(`{"handoffId":%q}`, strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")))
		},
		"non-v4 UUID": func(delivery *workerDeliveryFake) {
			delivery.payload = []byte(`{"handoffId":"67e55044-10b1-11f1-9f9b-325096b39f47"}`)
		},
		"trailing value": func(delivery *workerDeliveryFake) { delivery.payload = append(delivery.payload, []byte(` {}`)...) },
		"oversize": func(delivery *workerDeliveryFake) {
			delivery.payload = []byte(strings.Repeat(" ", maximumPendingHandoffPayloadBytes+1))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			delivery := pendingDelivery(handoffID)
			mutate(delivery)
			if _, err := decodePendingHandoff(delivery); err == nil {
				t.Fatal("invalid pending handoff envelope was accepted")
			}
		})
	}
}

func TestWorkerAcknowledgesOnlyValidatedCompletionOrSameIDInspection(t *testing.T) {
	record := testRecord(t)
	for name, service := range map[string]*workerServiceFake{
		"complete": {
			completeRecord: record, inspectRecord: record,
		},
		"commit unknown inspection": {
			completeErr: ErrOutcomeUnknown, inspectRecord: record,
		},
	} {
		t.Run(name, func(t *testing.T) {
			sink := &workerQuarantineFake{}
			worker := newWorkerForTest(t, service, sink)
			delivery := pendingDelivery(record.HandoffID)
			if err := worker.Handle(context.Background(), delivery); err != nil {
				t.Fatal(err)
			}
			if delivery.acks != 1 || delivery.naks != 0 || delivery.terms != 0 || len(sink.records) != 0 {
				t.Fatalf("delivery actions ack=%d nak=%d term=%d quarantine=%d", delivery.acks, delivery.naks, delivery.terms, len(sink.records))
			}
			if len(service.completeIDs) != 1 || service.completeIDs[0] != record.HandoffID {
				t.Fatalf("Complete IDs = %v", service.completeIDs)
			}
			if name == "commit unknown inspection" && (len(service.inspectIDs) != 1 || service.inspectIDs[0] != record.HandoffID) {
				t.Fatalf("Inspect IDs = %v", service.inspectIDs)
			}
		})
	}
}

func TestWorkerRetriesTransientClassesWithSameIDAndBoundedDelay(t *testing.T) {
	record := testRecord(t)
	for name, service := range map[string]*workerServiceFake{
		"not ready": {completeErr: ErrNotReady},
		"retryable": {completeErr: ErrRetryable},
		"unknown absent": {
			completeErr: ErrOutcomeUnknown, inspectErr: ErrNotFound,
		},
	} {
		t.Run(name, func(t *testing.T) {
			sink := &workerQuarantineFake{}
			worker := newWorkerForTest(t, service, sink)
			delivery := pendingDelivery(record.HandoffID)
			delivery.attempt = 3
			if err := worker.Handle(context.Background(), delivery); err != nil {
				t.Fatal(err)
			}
			if delivery.acks != 0 || delivery.naks != 1 || delivery.terms != 0 ||
				len(delivery.nakDelays) != 1 || delivery.nakDelays[0] != 15*time.Second || len(sink.records) != 0 {
				t.Fatalf("delivery actions ack=%d nak=%d delays=%v term=%d quarantine=%d", delivery.acks, delivery.naks, delivery.nakDelays, delivery.terms, len(sink.records))
			}
			if len(service.completeIDs) != 1 || service.completeIDs[0] != record.HandoffID {
				t.Fatalf("Complete IDs = %v", service.completeIDs)
			}
		})
	}
}

func TestWorkerQuarantinesInvalidConflictCorruptAndExhaustedDeliveriesBeforeTerm(t *testing.T) {
	record := testRecord(t)
	corrupt := cloneRecord(record)
	corrupt.Bundle.HandoffID = testUUID(30)
	tests := map[string]struct {
		service *workerServiceFake
		mutate  func(*workerDeliveryFake)
		reason  string
	}{
		"invalid message": {
			service: &workerServiceFake{},
			mutate:  func(delivery *workerDeliveryFake) { delivery.payload = []byte(`{"projectId":"forbidden"}`) },
			reason:  "invalid_message",
		},
		"conflict": {
			service: &workerServiceFake{completeErr: ErrConflict}, reason: "handoff_conflict",
		},
		"corrupt": {
			service: &workerServiceFake{completeRecord: corrupt}, reason: "corrupt_completion",
		},
		"retry exhausted": {
			service: &workerServiceFake{completeErr: ErrNotReady},
			mutate:  func(delivery *workerDeliveryFake) { delivery.attempt = 20 },
			reason:  "retry_exhausted_not_ready",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			sink := &workerQuarantineFake{}
			worker := newWorkerForTest(t, test.service, sink)
			delivery := pendingDelivery(record.HandoffID)
			if test.mutate != nil {
				test.mutate(delivery)
			}
			if err := worker.Handle(context.Background(), delivery); err != nil {
				t.Fatal(err)
			}
			if delivery.acks != 0 || delivery.naks != 0 || delivery.terms != 1 || len(sink.records) != 1 {
				t.Fatalf("delivery actions ack=%d nak=%d term=%d quarantine=%d", delivery.acks, delivery.naks, delivery.terms, len(sink.records))
			}
			terminal := sink.records[0]
			if terminal.Reason != test.reason || terminal.SchemaVersion != HandoffQuarantineSchemaV1 ||
				terminal.SourceMessageID != delivery.messageID || terminal.SourceStreamSequence != delivery.streamSequence ||
				terminal.SourcePayloadHash == "" || terminal.QuarantinedAt != "2026-07-20T12:00:00.123Z" {
				t.Fatalf("quarantine record = %#v", terminal)
			}
		})
	}
}

func TestWorkerDoesNotTermWhenQuarantineFails(t *testing.T) {
	record := testRecord(t)
	sink := &workerQuarantineFake{err: errors.New("quarantine unavailable")}
	worker := newWorkerForTest(t, &workerServiceFake{completeErr: ErrConflict}, sink)
	delivery := pendingDelivery(record.HandoffID)
	delivery.attempt = uint64(worker.config.TerminalAttemptThreshold + 100)
	err := worker.Handle(context.Background(), delivery)
	if err == nil || !strings.Contains(err.Error(), "quarantine unavailable") {
		t.Fatalf("Handle() error = %v", err)
	}
	if delivery.acks != 0 || delivery.naks != 1 || delivery.terms != 0 || len(sink.records) != 1 {
		t.Fatalf("delivery actions ack=%d nak=%d term=%d quarantine=%d", delivery.acks, delivery.naks, delivery.terms, len(sink.records))
	}
}

func TestRequiredConsumerAckWaitIncludesServiceRecoveryInspectTerminalAndAck(t *testing.T) {
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

func TestWorkerCancellationNeverAcknowledges(t *testing.T) {
	record := testRecord(t)
	ctx, cancel := context.WithCancel(context.Background())
	service := &workerServiceFake{complete: func(context.Context, uuid.UUID) (Record, error) {
		cancel()
		return cloneRecord(record), nil
	}}
	worker := newWorkerForTest(t, service, &workerQuarantineFake{})
	delivery := pendingDelivery(record.HandoffID)
	if err := worker.Handle(ctx, delivery); !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle() error = %v", err)
	}
	if delivery.acks != 0 || delivery.naks != 0 || delivery.terms != 0 {
		t.Fatalf("cancelled delivery actions ack=%d nak=%d term=%d", delivery.acks, delivery.naks, delivery.terms)
	}
}

func TestWorkerCancellationAfterQuarantineWriteDoesNotTerminateSource(t *testing.T) {
	record := testRecord(t)
	ctx, cancel := context.WithCancel(context.Background())
	sink := &workerQuarantineFake{call: func(context.Context, QuarantineRecord) error {
		cancel()
		return nil
	}}
	worker := newWorkerForTest(t, &workerServiceFake{}, sink)
	delivery := pendingDelivery(record.HandoffID)
	delivery.payload = []byte(`{"projectId":"forbidden"}`)
	if err := worker.Handle(ctx, delivery); !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle() error = %v", err)
	}
	if delivery.acks != 0 || delivery.naks != 0 || delivery.terms != 0 || len(sink.records) != 1 {
		t.Fatalf("cancelled terminal actions ack=%d nak=%d term=%d quarantine=%d", delivery.acks, delivery.naks, delivery.terms, len(sink.records))
	}
}

func TestWorkerOperationTimeoutNaksSameDelivery(t *testing.T) {
	record := testRecord(t)
	service := &workerServiceFake{complete: func(ctx context.Context, _ uuid.UUID) (Record, error) {
		<-ctx.Done()
		return Record{}, ctx.Err()
	}}
	worker := newWorkerForTest(t, service, &workerQuarantineFake{})
	worker.config.OperationTimeout = time.Millisecond
	delivery := pendingDelivery(record.HandoffID)
	if err := worker.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.acks != 0 || delivery.naks != 1 || delivery.terms != 0 {
		t.Fatalf("timed out delivery actions ack=%d nak=%d term=%d", delivery.acks, delivery.naks, delivery.terms)
	}
}

func TestNewWorkerRequiresMatchingBoundedConsumerPolicy(t *testing.T) {
	config := DefaultWorkerConfig()
	ackConfirmWait := time.Second
	requiredAckWait, err := requiredConsumerAckWait(config, ackConfirmWait)
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord(t)
	service := &workerServiceFake{completeRecord: record}
	sink := &workerQuarantineFake{}
	base := ConsumerLimits{
		MaxDeliver: -1, MaxAckPending: config.MaxInFlight,
		AckWait: requiredAckWait, AckConfirmWait: ackConfirmWait,
		MaxRequestBatch:    config.MaxInFlight,
		MaxRequestMaxBytes: config.MaxInFlight * (maximumPendingHandoffPayloadBytes + maximumBrokerHeaderBytes),
	}
	for name, limits := range map[string]ConsumerLimits{
		"finite max deliver":    func() ConsumerLimits { value := base; value.MaxDeliver = 20; return value }(),
		"unbounded ack pending": func() ConsumerLimits { value := base; value.MaxAckPending = 0; return value }(),
		"too little capacity":   func() ConsumerLimits { value := base; value.MaxAckPending--; return value }(),
		"short request batch":   func() ConsumerLimits { value := base; value.MaxRequestBatch--; return value }(),
		"short request bytes":   func() ConsumerLimits { value := base; value.MaxRequestMaxBytes--; return value }(),
		"short ack wait":        func() ConsumerLimits { value := base; value.AckWait--; return value }(),
		"missing ack confirm":   func() ConsumerLimits { value := base; value.AckConfirmWait = 0; return value }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewWorker(service, &workerConsumerFake{limits: limits}, sink, config); err == nil {
				t.Fatal("mismatched consumer policy was accepted")
			}
		})
	}
}

func TestWorkerRunOnceUsesBoundedBatch(t *testing.T) {
	record := testRecord(t)
	config := DefaultWorkerConfig()
	config.MaxInFlight = 2
	ackConfirmWait := time.Second
	requiredAckWait, err := requiredConsumerAckWait(config, ackConfirmWait)
	if err != nil {
		t.Fatal(err)
	}
	consumer := &workerConsumerFake{
		limits: ConsumerLimits{
			MaxDeliver: -1, MaxAckPending: 2,
			AckWait: requiredAckWait, AckConfirmWait: ackConfirmWait,
			MaxRequestBatch:    2,
			MaxRequestMaxBytes: 2 * (maximumPendingHandoffPayloadBytes + maximumBrokerHeaderBytes),
		},
		deliveries: []Delivery{pendingDelivery(record.HandoffID), pendingDelivery(record.HandoffID)},
	}
	worker, err := NewWorker(&workerServiceFake{completeRecord: record}, consumer, &workerQuarantineFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil || processed != 2 || consumer.maximum != 2 {
		t.Fatalf("RunOnce() = %d, %v; maximum=%d", processed, err, consumer.maximum)
	}
}
