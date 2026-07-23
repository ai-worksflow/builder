package workflowqualificationactivation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type streamInfoFake struct {
	info *nats.StreamInfo
	err  error
}

func (fake streamInfoFake) StreamInfo(string, ...nats.JSOpt) (*nats.StreamInfo, error) {
	return fake.info, fake.err
}

func validActivationStreamInfo() *nats.StreamInfo {
	return &nats.StreamInfo{Config: nats.StreamConfig{
		Name: DefaultActivationEventStream, Subjects: []string{"worksflow.>"},
		Storage: nats.FileStorage, Retention: nats.LimitsPolicy, Discard: nats.DiscardOld,
		MaxMsgs: -1, MaxBytes: -1, MaxMsgsPerSubject: -1, MaxMsgSize: -1,
		MaxAge: 30 * 24 * time.Hour, Duplicates: 10 * time.Minute, Replicas: 1,
	}}
}

func activationPostureRequirements() NATSPostureRequirements {
	return NATSPostureRequirements{
		Stream: DefaultActivationEventStream, SourceSubject: WorkflowRunEventSubject,
		QuarantineSubject: ActivationQuarantineSubject, Durable: ActivationDurable,
		MaximumEventPayloadBytes: maximumNodeCompletedPayloadBytes,
		MaximumQuarantineBytes:   maximumQuarantineWireBytes,
		MinimumStreamMaxMsgBytes: maximumNodeCompletedPayloadBytes + maximumBrokerHeaderBytes,
		RequiredStorage:          nats.FileStorage, RequiredRetention: nats.LimitsPolicy,
		RequiredDiscard: nats.DiscardOld, MinimumRetention: minimumActivationRetention,
		MinimumDuplicateWindow: minimumActivationDedupe, MinimumReplicas: 1,
		RequiredMaxDeliver: -1, RequiredAckPolicy: nats.AckExplicitPolicy,
		RequiredDeliverPolicy: nats.DeliverAllPolicy,
	}
}

func TestJetStreamReadinessAcceptsClosedActivationStream(t *testing.T) {
	readiness, err := NewJetStreamReadiness(streamInfoFake{info: validActivationStreamInfo()})
	if err != nil {
		t.Fatal(err)
	}
	if err := readiness.AssertReady(context.Background(), activationPostureRequirements()); err != nil {
		t.Fatal(err)
	}
}

func TestJetStreamReadinessRejectsNarrowOrMutablePosture(t *testing.T) {
	tests := map[string]func(*nats.StreamConfig){
		"missing quarantine subject": func(config *nats.StreamConfig) {
			config.Subjects = []string{WorkflowRunEventSubject}
		},
		"memory storage":         func(config *nats.StreamConfig) { config.Storage = nats.MemoryStorage },
		"short retention":        func(config *nats.StreamConfig) { config.MaxAge = time.Hour },
		"short duplicate window": func(config *nats.StreamConfig) { config.Duplicates = time.Minute },
		"small messages":         func(config *nats.StreamConfig) { config.MaxMsgSize = 1024 },
		"count eviction":         func(config *nats.StreamConfig) { config.MaxMsgs = 1000 },
		"byte eviction":          func(config *nats.StreamConfig) { config.MaxBytes = 1 << 20 },
		"per-subject eviction":   func(config *nats.StreamConfig) { config.MaxMsgsPerSubject = 100 },
		"rollup":                 func(config *nats.StreamConfig) { config.AllowRollup = true },
		"sealed":                 func(config *nats.StreamConfig) { config.Sealed = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			info := validActivationStreamInfo()
			mutate(&info.Config)
			readiness, err := NewJetStreamReadiness(streamInfoFake{info: info})
			if err != nil {
				t.Fatal(err)
			}
			if err := readiness.AssertReady(context.Background(), activationPostureRequirements()); err == nil {
				t.Fatal("narrow stream posture was accepted")
			}
		})
	}
}

func TestJetStreamReadinessPropagatesInspectionAndContextFailures(t *testing.T) {
	want := errors.New("unavailable")
	readiness, err := NewJetStreamReadiness(streamInfoFake{err: want})
	if err != nil {
		t.Fatal(err)
	}
	if err := readiness.AssertReady(context.Background(), activationPostureRequirements()); !errors.Is(err, want) {
		t.Fatalf("inspection error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := readiness.AssertReady(cancelled, activationPostureRequirements()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled readiness error = %v", err)
	}
}

func TestStreamCapturesSubjectUsesOnlyCanonicalNATSWildcards(t *testing.T) {
	for _, test := range []struct {
		filters []string
		subject string
		want    bool
	}{
		{[]string{"worksflow.>"}, WorkflowRunEventSubject, true},
		{[]string{"worksflow.workflow.*.event"}, WorkflowRunEventSubject, true},
		{[]string{WorkflowRunEventSubject}, WorkflowRunEventSubject, true},
		{[]string{"worksflow.workflow.>"}, "worksflow.workflow", false},
		{[]string{"worksflow.*"}, WorkflowRunEventSubject, false},
		{[]string{"worksflow.>.event"}, WorkflowRunEventSubject, false},
	} {
		if got := streamCapturesSubject(test.filters, test.subject); got != test.want {
			t.Fatalf("filters=%s subject=%q got=%t want=%t", strings.Join(test.filters, ","), test.subject, got, test.want)
		}
	}
}
