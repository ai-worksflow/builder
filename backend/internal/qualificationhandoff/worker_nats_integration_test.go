package qualificationhandoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestNATSHandoffWorkerIntegration(t *testing.T) {
	url := os.Getenv("WORKSFLOW_QUALIFICATION_HANDOFF_NATS_TEST_URL")
	if url == "" {
		t.Skip("WORKSFLOW_QUALIFICATION_HANDOFF_NATS_TEST_URL is not configured")
	}
	connection, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	jetStream, err := connection.JetStream(nats.MaxWait(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stream := "QH_" + strings.ToUpper(strings.ReplaceAll(testUUID(55), "-", ""))
	if _, err := jetStream.AddStream(&nats.StreamConfig{
		Name: stream, Subjects: []string{PendingHandoffSubject, HandoffQuarantineSubject},
		Storage: nats.FileStorage, Retention: nats.LimitsPolicy, Discard: nats.DiscardOld,
		MaxAge: minimumHandoffRetention, Duplicates: minimumHandoffDedupe, Replicas: 1,
		MaxMsgSize: int32(max(maximumPendingHandoffPayloadBytes, maximumQuarantineWireBytes) + maximumBrokerHeaderBytes),
	}); err != nil {
		t.Fatal(err)
	}
	defer jetStream.DeleteStream(stream)

	consumerConfig := DefaultNATSConsumerConfig()
	consumerConfig.Stream = stream
	consumerConfig.AckWait = 10 * time.Second
	consumerConfig.AckBackOff = []time.Duration{10 * time.Second, 20 * time.Second}
	consumerConfig.MaxAckPending = 1
	consumerConfig.MaxRequestBatch = 1
	consumerConfig.MaxRequestExpires = time.Second
	consumerConfig.FetchWait = time.Second
	consumerConfig.AckConfirmWait = time.Second
	consumer, err := NewNATSConsumer(
		context.Background(), jetStream, integrationNATSReadiness{jetStream: jetStream}, consumerConfig,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	quarantine, err := NewNATSQuarantineSink(jetStream, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord(t)
	service := &workerServiceFake{completeRecord: record}
	workerConfig := DefaultWorkerConfig()
	workerConfig.MaxInFlight = 1
	workerConfig.TerminalAttemptThreshold = 4
	workerConfig.OperationTimeout = time.Second
	workerConfig.TerminalTimeout = time.Second
	workerConfig.RetryBackOff = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}
	worker, err := NewWorker(service, consumer, quarantine, workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	consumerInfo, err := jetStream.ConsumerInfo(stream, PendingHandoffDurable)
	if err != nil {
		t.Fatal(err)
	}
	if consumerInfo.Config.MaxDeliver != unlimitedNATSDeliveries ||
		consumerInfo.Config.MaxRequestBatch != consumerConfig.MaxRequestBatch ||
		consumerInfo.Config.MaxRequestMaxBytes != consumerConfig.MaxRequestMaxBytes {
		t.Fatalf("durable consumer limits = %#v", consumerInfo.Config)
	}

	publishPending := func(messageID, payload string) {
		t.Helper()
		message := nats.NewMsg(PendingHandoffSubject)
		message.Data = []byte(payload)
		message.Header.Set(nats.MsgIdHdr, messageID)
		message.Header.Set(handoffEventTypeHeader, PendingHandoffEventType)
		if _, err := jetStream.PublishMsg(message); err != nil {
			t.Fatal(err)
		}
	}
	publishPending(testUUID(56), fmt.Sprintf(`{"handoffId":%q}`, record.HandoffID.String()))
	processed, err := worker.RunOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("valid RunOnce() = %d, %v", processed, err)
	}
	if len(service.completeIDs) != 1 || service.completeIDs[0] != record.HandoffID {
		t.Fatalf("Complete IDs = %v", service.completeIDs)
	}

	// A failed durable quarantine at the worker's logical terminal threshold
	// must remain broker-retryable because the durable has no delivery ceiling.
	service.mu.Lock()
	service.completeErr = ErrRetryable
	service.mu.Unlock()
	worker.config.TerminalAttemptThreshold = 1
	failingQuarantine := &workerQuarantineFake{err: errors.New("quarantine unavailable")}
	worker.quarantine = failingQuarantine
	publishPending(testUUID(58), fmt.Sprintf(`{"handoffId":%q}`, record.HandoffID.String()))
	processed, err = worker.RunOnce(context.Background())
	if err == nil || processed != 1 || !strings.Contains(err.Error(), "quarantine unavailable") ||
		len(failingQuarantine.records) != 1 {
		t.Fatalf("failed quarantine RunOnce() = %d, %v records=%d", processed, err, len(failingQuarantine.records))
	}
	worker.quarantine = quarantine
	processed, err = worker.RunOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("redelivered terminal RunOnce() = %d, %v", processed, err)
	}
	service.mu.Lock()
	service.completeErr = nil
	completeCountBeforeInvalid := len(service.completeIDs)
	service.mu.Unlock()
	worker.config.TerminalAttemptThreshold = 4

	publishPending(testUUID(57), fmt.Sprintf(`{"handoffId":%q,"projectId":%q}`, record.HandoffID.String(), testUUID(4)))
	processed, err = worker.RunOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("invalid RunOnce() = %d, %v", processed, err)
	}
	service.mu.Lock()
	completeCountAfterInvalid := len(service.completeIDs)
	service.mu.Unlock()
	if completeCountAfterInvalid != completeCountBeforeInvalid {
		t.Fatalf("invalid envelope reached Complete: before=%d after=%d", completeCountBeforeInvalid, completeCountAfterInvalid)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		info, infoErr := jetStream.ConsumerInfo(stream, PendingHandoffDurable)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.NumAckPending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer retained %d unacknowledged messages", info.NumAckPending)
		}
		time.Sleep(10 * time.Millisecond)
	}
	quarantined, err := jetStream.GetLastMsg(stream, HandoffQuarantineSubject)
	if err != nil {
		t.Fatal(err)
	}
	var terminal QuarantineRecord
	if err := json.Unmarshal(quarantined.Data, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.SchemaVersion != HandoffQuarantineSchemaV1 || terminal.Reason != "invalid_message" ||
		terminal.SourceMessageID != testUUID(57) || terminal.HandoffID != nil {
		t.Fatalf("quarantine record = %#v", terminal)
	}
}

type integrationNATSReadiness struct {
	jetStream nats.JetStreamContext
}

func (readiness integrationNATSReadiness) AssertReady(ctx context.Context, requirement NATSPostureRequirements) error {
	if readiness.jetStream == nil {
		return errors.New("JetStream is required")
	}
	info, err := readiness.jetStream.StreamInfo(requirement.Stream, nats.Context(ctx))
	if err != nil {
		return err
	}
	config := info.Config
	if config.Storage != requirement.RequiredStorage || config.Retention != requirement.RequiredRetention ||
		config.Discard != requirement.RequiredDiscard || config.MaxAge < requirement.MinimumRetention ||
		config.Duplicates < requirement.MinimumDuplicateWindow || config.Replicas < requirement.MinimumReplicas ||
		config.MaxMsgSize < int32(requirement.MinimumStreamMaxMsgBytes) ||
		!slices.Contains(config.Subjects, requirement.SourceSubject) ||
		!slices.Contains(config.Subjects, requirement.QuarantineSubject) {
		return errors.New("stream posture does not meet qualification handoff requirements")
	}
	return nil
}
