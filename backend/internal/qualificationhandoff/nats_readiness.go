package qualificationhandoff

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"
)

type natsStreamInfoAPI interface {
	StreamInfo(string, ...nats.JSOpt) (*nats.StreamInfo, error)
}

// JetStreamReadiness proves the durable pending-handoff/quarantine stream
// posture before NewNATSConsumer creates or binds the frozen durable.
type JetStreamReadiness struct {
	jetStream natsStreamInfoAPI
}

func NewJetStreamReadiness(jetStream natsStreamInfoAPI) (*JetStreamReadiness, error) {
	if isNilInterface(jetStream) {
		return nil, errors.New("qualification handoff JetStream readiness requires a stream inspector")
	}
	return &JetStreamReadiness{jetStream: jetStream}, nil
}

func (readiness *JetStreamReadiness) AssertReady(
	ctx context.Context,
	requirements NATSPostureRequirements,
) error {
	if readiness == nil || isNilInterface(readiness.jetStream) || isNilInterface(ctx) {
		return errors.New("qualification handoff JetStream readiness and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNATSPostureRequirements(requirements); err != nil {
		return err
	}
	info, err := readiness.jetStream.StreamInfo(requirements.Stream, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("inspect qualification handoff JetStream %q: %w", requirements.Stream, err)
	}
	if info == nil {
		return errors.New("qualification handoff JetStream inspection returned no stream information")
	}
	config := info.Config
	if config.Name != requirements.Stream {
		return errors.New("qualification handoff JetStream returned a different stream identity")
	}
	if !streamCapturesSubject(config.Subjects, requirements.SourceSubject) ||
		!streamCapturesSubject(config.Subjects, requirements.QuarantineSubject) {
		return errors.New("qualification handoff JetStream does not durably own both source and quarantine subjects")
	}
	if config.Storage != requirements.RequiredStorage ||
		config.Retention != requirements.RequiredRetention ||
		config.Discard != requirements.RequiredDiscard {
		return errors.New("qualification handoff JetStream storage, retention, or discard posture differs")
	}
	if config.MaxAge > 0 && config.MaxAge < requirements.MinimumRetention {
		return errors.New("qualification handoff JetStream maximum age is shorter than the required retention")
	}
	if config.Duplicates < requirements.MinimumDuplicateWindow {
		return errors.New("qualification handoff JetStream duplicate window is too short")
	}
	if config.MaxMsgSize > 0 && int(config.MaxMsgSize) < requirements.MinimumStreamMaxMsgBytes {
		return errors.New("qualification handoff JetStream maximum message size is too small")
	}
	if config.Replicas < requirements.MinimumReplicas {
		return errors.New("qualification handoff JetStream replica count is too small")
	}
	if config.MaxMsgs != -1 || config.MaxBytes != -1 || config.MaxMsgsPerSubject != -1 {
		return errors.New("qualification handoff JetStream count and byte retention caps must be unlimited")
	}
	if config.NoAck || config.Sealed || config.AllowRollup || config.Mirror != nil ||
		len(config.Sources) != 0 || config.SubjectTransform != nil || config.RePublish != nil {
		return errors.New("qualification handoff JetStream has an incompatible acknowledgement, sealing, rollup, or transform policy")
	}
	return nil
}

func validateNATSPostureRequirements(requirements NATSPostureRequirements) error {
	if strings.TrimSpace(requirements.Stream) == "" ||
		strings.TrimSpace(requirements.SourceSubject) == "" ||
		strings.TrimSpace(requirements.QuarantineSubject) == "" ||
		strings.TrimSpace(requirements.Durable) == "" ||
		requirements.SourceSubject == requirements.QuarantineSubject ||
		requirements.MaximumEventPayloadBytes <= 0 ||
		requirements.MaximumQuarantineBytes <= 0 ||
		requirements.MinimumStreamMaxMsgBytes < requirements.MaximumEventPayloadBytes ||
		requirements.MinimumStreamMaxMsgBytes < requirements.MaximumQuarantineBytes ||
		requirements.MinimumRetention <= 0 || requirements.MinimumDuplicateWindow <= 0 ||
		requirements.MinimumReplicas <= 0 || requirements.RequiredMaxDeliver != -1 ||
		requirements.RequiredAckPolicy != nats.AckExplicitPolicy ||
		requirements.RequiredDeliverPolicy != nats.DeliverAllPolicy {
		return errors.New("qualification handoff JetStream posture requirements are invalid or weakened")
	}
	return nil
}

func streamCapturesSubject(filters []string, subject string) bool {
	if strings.TrimSpace(subject) == "" || strings.ContainsAny(subject, "*>") {
		return false
	}
	subjectTokens := strings.Split(subject, ".")
	for _, filter := range filters {
		filterTokens := strings.Split(filter, ".")
		matched := true
		for index, token := range filterTokens {
			switch token {
			case ">":
				if index != len(filterTokens)-1 || index >= len(subjectTokens) {
					matched = false
				}
				if matched {
					return true
				}
			case "*":
				if index >= len(subjectTokens) {
					matched = false
				}
			default:
				if token == "" || index >= len(subjectTokens) || token != subjectTokens[index] {
					matched = false
				}
			}
			if !matched {
				break
			}
		}
		if matched && len(filterTokens) == len(subjectTokens) {
			return true
		}
	}
	return false
}

var _ NATSReadiness = (*JetStreamReadiness)(nil)
