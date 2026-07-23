package qualificationhandoff

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type handoffStreamInfoFake struct {
	info *nats.StreamInfo
	err  error
}

func (fake handoffStreamInfoFake) StreamInfo(string, ...nats.JSOpt) (*nats.StreamInfo, error) {
	return fake.info, fake.err
}

func validHandoffStreamInfo() *nats.StreamInfo {
	return &nats.StreamInfo{Config: nats.StreamConfig{
		Name: DefaultHandoffEventStream, Subjects: []string{"worksflow.>"},
		Storage: nats.FileStorage, Retention: nats.LimitsPolicy, Discard: nats.DiscardOld,
		MaxMsgs: -1, MaxBytes: -1, MaxMsgsPerSubject: -1, MaxMsgSize: -1,
		MaxAge: 30 * 24 * time.Hour, Duplicates: 10 * time.Minute, Replicas: 1,
	}}
}

func handoffPostureRequirements() NATSPostureRequirements {
	return NATSPostureRequirements{
		Stream: DefaultHandoffEventStream, SourceSubject: PendingHandoffSubject,
		QuarantineSubject: HandoffQuarantineSubject, Durable: PendingHandoffDurable,
		MaximumEventPayloadBytes: maximumPendingHandoffPayloadBytes,
		MaximumQuarantineBytes:   maximumQuarantineWireBytes,
		MinimumStreamMaxMsgBytes: max(maximumPendingHandoffPayloadBytes, maximumQuarantineWireBytes) + maximumBrokerHeaderBytes,
		RequiredStorage:          nats.FileStorage,
		RequiredRetention:        nats.LimitsPolicy,
		RequiredDiscard:          nats.DiscardOld,
		MinimumRetention:         minimumHandoffRetention,
		MinimumDuplicateWindow:   minimumHandoffDedupe,
		MinimumReplicas:          1,
		RequiredMaxDeliver:       -1,
		RequiredAckPolicy:        nats.AckExplicitPolicy,
		RequiredDeliverPolicy:    nats.DeliverAllPolicy,
	}
}

func TestJetStreamReadinessAcceptsClosedHandoffStream(t *testing.T) {
	readiness, err := NewJetStreamReadiness(handoffStreamInfoFake{info: validHandoffStreamInfo()})
	if err != nil {
		t.Fatal(err)
	}
	if err := readiness.AssertReady(context.Background(), handoffPostureRequirements()); err != nil {
		t.Fatal(err)
	}
}

func TestJetStreamReadinessRejectsNarrowHandoffStream(t *testing.T) {
	tests := map[string]func(*nats.StreamConfig){
		"missing source": func(config *nats.StreamConfig) {
			config.Subjects = []string{HandoffQuarantineSubject}
		},
		"memory":          func(config *nats.StreamConfig) { config.Storage = nats.MemoryStorage },
		"short retention": func(config *nats.StreamConfig) { config.MaxAge = time.Hour },
		"short dedupe":    func(config *nats.StreamConfig) { config.Duplicates = time.Minute },
		"small message":   func(config *nats.StreamConfig) { config.MaxMsgSize = 1024 },
		"bounded bytes":   func(config *nats.StreamConfig) { config.MaxBytes = 1 << 20 },
		"rollup":          func(config *nats.StreamConfig) { config.AllowRollup = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			info := validHandoffStreamInfo()
			mutate(&info.Config)
			readiness, err := NewJetStreamReadiness(handoffStreamInfoFake{info: info})
			if err != nil {
				t.Fatal(err)
			}
			if err := readiness.AssertReady(context.Background(), handoffPostureRequirements()); err == nil {
				t.Fatal("narrow stream posture was accepted")
			}
		})
	}
}

func TestJetStreamReadinessPropagatesHandoffInspectionFailure(t *testing.T) {
	want := errors.New("unavailable")
	readiness, err := NewJetStreamReadiness(handoffStreamInfoFake{err: want})
	if err != nil {
		t.Fatal(err)
	}
	if err := readiness.AssertReady(context.Background(), handoffPostureRequirements()); !errors.Is(err, want) {
		t.Fatalf("inspection error = %v", err)
	}
}
