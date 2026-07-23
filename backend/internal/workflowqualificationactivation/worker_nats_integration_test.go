package workflowqualificationactivation

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestNATSActivationWorkerIntegration(t *testing.T) {
	url := os.Getenv("WORKSFLOW_QUALIFICATION_ACTIVATION_NATS_TEST_URL")
	if url == "" {
		t.Skip("WORKSFLOW_QUALIFICATION_ACTIVATION_NATS_TEST_URL is not configured")
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
	stream := "WQA_" + strings.ToUpper(strings.ReplaceAll(testCompletionEventID, "-", ""))
	if _, err := jetStream.AddStream(&nats.StreamConfig{
		Name:     stream,
		Subjects: []string{WorkflowRunEventSubject, ActivationQuarantineSubject},
		Storage:  nats.FileStorage, Retention: nats.LimitsPolicy, Discard: nats.DiscardOld,
		MaxAge: minimumActivationRetention, Duplicates: minimumActivationDedupe,
		MaxMsgSize: int32(maximumNodeCompletedPayloadBytes + maximumBrokerHeaderBytes),
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
	consumer, err := NewNATSConsumer(context.Background(), jetStream, &natsReadinessFake{}, consumerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	quarantine, err := NewNATSQuarantineSink(jetStream, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseCompletionEventID(testCompletionEventID)
	service := &workerServiceFake{record: ignoredRecord(id)}
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

	publish := func(messageID, eventType string, payload []byte) {
		t.Helper()
		message := nats.NewMsg(WorkflowRunEventSubject)
		message.Data = payload
		message.Header.Set(nats.MsgIdHdr, messageID)
		message.Header.Set(workflowEventTypeHeader, eventType)
		if _, err := jetStream.PublishMsg(message); err != nil {
			t.Fatal(err)
		}
	}
	valid := nodeCompletedDelivery(t)
	publish(valid.messageID, NodeCompletedEventType, valid.payload)
	processed, err := worker.RunOnce(context.Background())
	if err != nil || processed != 1 || service.activateCalls != 1 || service.activateID != id {
		t.Fatalf("valid RunOnce() = %d, %v service=%#v", processed, err, service)
	}

	invalidMessageID := "50505050-5050-4050-8050-505050505050"
	publish(invalidMessageID, NodeCompletedEventType, []byte(`{"id":"forged"}`))
	processed, err = worker.RunOnce(context.Background())
	if err != nil || processed != 1 || service.activateCalls != 1 {
		t.Fatalf("invalid RunOnce() = %d, %v service=%#v", processed, err, service)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		info, infoErr := jetStream.ConsumerInfo(stream, ActivationDurable)
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
	quarantined, err := jetStream.GetLastMsg(stream, ActivationQuarantineSubject)
	if err != nil {
		t.Fatal(err)
	}
	var terminal QuarantineRecord
	if err := json.Unmarshal(quarantined.Data, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.SchemaVersion != ActivationQuarantineSchemaV1 || terminal.Reason != "invalid_message" ||
		terminal.SourceMessageID != invalidMessageID || terminal.CompletionEventID != nil {
		t.Fatalf("quarantine record = %#v", terminal)
	}
}
