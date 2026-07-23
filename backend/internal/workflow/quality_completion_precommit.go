package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

const qualityCompletionCommitUnknownInspectTimeout = 5 * time.Second

var (
	ErrQualityCompletionOutcomeUnknown   = errors.New("workflow v3 Quality completion outcome is unknown; inspect the same precommit")
	ErrQualityCompletionPrecommitCorrupt = errors.New("workflow v3 Quality completion precommit is corrupt")
	ErrQualityCompletionPrecommitStale   = errors.New("workflow v3 Quality completion precommit closure is stale")
	ErrQualityCompletionPrecommitClosure = errors.New("workflow v3 Quality completion transaction closure is incomplete")
	ErrQualityCompletionRetryable        = errors.New("workflow v3 Quality completion transaction is retryable")
)

// QualityCompletionPrecommitMutation is the closed, lossless hand-off from
// the v3 runtime to Store.Commit. It deliberately contains values only: an
// arbitrary callback would let application code widen the database transaction
// after the store has acquired its authoritative locks.
type QualityCompletionPrecommitMutation struct {
	PrecommitID              string
	WorkflowInputOperationID string
	WorkflowInputAuthorityID string
	ActivationEventID        string
	ProjectID                string
	WorkflowRunID            string
	QualityNodeRunID         string
	QualityNodeKey           string
	GateNodeRunID            string
	GateNodeKey              string
	ExpectedRunCursor        uint64
	CompletionEventID        string
	CompletionEventSequence  uint64
	CompletionEventPayload   json.RawMessage
	CompletionEventActorID   string
	LeaseOwner               string
	LeaseAttempt             int
	CompletedAt              time.Time
	OutputRevisionID         string
	GateInputCanonical       json.RawMessage
	GateInputRawHash         string
	GateInputRawSize         int64
	GateInputSemanticHash    string
	GateInputBindingCount    int
}

func cloneQualityCompletionPrecommitMutation(value *QualityCompletionPrecommitMutation) *QualityCompletionPrecommitMutation {
	if value == nil {
		return nil
	}
	clone := *value
	clone.CompletionEventPayload = cloneRaw(value.CompletionEventPayload)
	clone.GateInputCanonical = cloneRaw(value.GateInputCanonical)
	return &clone
}

func newQualityCompletionPrecommitMutation(
	precommitID string,
	workflowInputOperationID string,
	workflowInputAuthorityID string,
	activationEventID string,
	run *RunRecord,
	quality *NodeRecord,
	gate *NodeRecord,
	lease Lease,
	event Event,
	gateInput domain.NodeInputEnvelope,
) (*QualityCompletionPrecommitMutation, error) {
	if run == nil || quality == nil || gate == nil || quality.CompletedAt == nil {
		return nil, domain.ErrInvalidArgument
	}
	raw := gateInput.Canonical()
	mutation := &QualityCompletionPrecommitMutation{
		PrecommitID:              precommitID,
		WorkflowInputOperationID: workflowInputOperationID,
		WorkflowInputAuthorityID: workflowInputAuthorityID,
		ActivationEventID:        activationEventID,
		ProjectID:                run.ProjectID, WorkflowRunID: run.ID,
		QualityNodeRunID: quality.ID, QualityNodeKey: quality.Key,
		GateNodeRunID: gate.ID, GateNodeKey: gate.Key,
		ExpectedRunCursor: run.EventCursor,
		CompletionEventID: event.ID, CompletionEventSequence: event.Sequence,
		CompletionEventPayload: cloneRaw(event.Payload), CompletionEventActorID: event.ActorID,
		LeaseOwner: lease.WorkerID, LeaseAttempt: lease.Attempt,
		CompletedAt: *quality.CompletedAt, OutputRevisionID: quality.OutputRevisionID,
		GateInputCanonical: raw, GateInputRawHash: qualityCompletionRawSHA256(raw),
		GateInputRawSize: int64(len(raw)), GateInputSemanticHash: normalizeQualityCompletionSHA256(gateInput.Hash()),
		GateInputBindingCount: len(gateInput.Bindings()),
	}
	if err := mutation.Validate(); err != nil {
		return nil, err
	}
	return mutation, nil
}

func (value QualityCompletionPrecommitMutation) Validate() error {
	requiredIdentities := []struct {
		field string
		value string
	}{
		{field: "precommitId", value: value.PrecommitID},
		{field: "workflowInputOperationId", value: value.WorkflowInputOperationID},
		{field: "workflowInputAuthorityId", value: value.WorkflowInputAuthorityID},
		{field: "activationEventId", value: value.ActivationEventID},
		{field: "projectId", value: value.ProjectID},
		{field: "workflowRunId", value: value.WorkflowRunID},
		{field: "qualityNodeRunId", value: value.QualityNodeRunID},
		{field: "gateNodeRunId", value: value.GateNodeRunID},
		{field: "completionEventId", value: value.CompletionEventID},
		{field: "outputRevisionId", value: value.OutputRevisionID},
	}
	for _, identity := range requiredIdentities {
		if !qualityCompletionUUIDv4(identity.value) {
			return fmt.Errorf("Quality completion %s must be a canonical UUIDv4", identity.field)
		}
	}
	if value.CompletionEventActorID != "" && !qualityCompletionUUIDv4(value.CompletionEventActorID) {
		return fmt.Errorf("Quality completion event actor must be a canonical UUIDv4")
	}
	activationIdentities := requiredIdentities[1:4]
	otherIdentities := append([]struct {
		field string
		value string
	}{}, requiredIdentities[:1]...)
	otherIdentities = append(otherIdentities, requiredIdentities[4:]...)
	if value.CompletionEventActorID != "" {
		otherIdentities = append(otherIdentities, struct {
			field string
			value string
		}{field: "completionEventActorId", value: value.CompletionEventActorID})
	}
	for index, identity := range activationIdentities {
		for _, other := range activationIdentities[index+1:] {
			if identity.value == other.value {
				return fmt.Errorf("Quality completion %s must be distinct from %s", identity.field, other.field)
			}
		}
		for _, other := range otherIdentities {
			if identity.value == other.value {
				return fmt.Errorf("Quality completion %s must be distinct from %s", identity.field, other.field)
			}
		}
	}
	if strings.TrimSpace(value.QualityNodeKey) == "" || strings.TrimSpace(value.GateNodeKey) == "" ||
		value.QualityNodeKey == value.GateNodeKey || strings.TrimSpace(value.LeaseOwner) == "" || value.LeaseAttempt < 1 {
		return fmt.Errorf("Quality completion node and lease identities are invalid")
	}
	if value.ExpectedRunCursor >= uint64(maximumJavaScriptSafeIntegerV3) ||
		value.CompletionEventSequence != value.ExpectedRunCursor+1 {
		return fmt.Errorf("Quality completion event sequence does not close its expected cursor")
	}
	if value.CompletedAt.IsZero() || value.CompletedAt.Location() != time.UTC ||
		!value.CompletedAt.Equal(value.CompletedAt.Truncate(time.Millisecond)) {
		return fmt.Errorf("Quality completion timestamp must be non-zero UTC at exact millisecond precision")
	}
	if len(value.CompletionEventPayload) == 0 || len(value.CompletionEventPayload) > maximumQualityResultV3Bytes ||
		!json.Valid(value.CompletionEventPayload) || rejectDuplicateJSONNamesV3(value.CompletionEventPayload) != nil {
		return fmt.Errorf("Quality completion event payload is invalid")
	}
	var eventPayload map[string]json.RawMessage
	if err := strictDecodeJSONV3(value.CompletionEventPayload, &eventPayload); err != nil || eventPayload["attempt"] == nil {
		return fmt.Errorf("Quality completion event payload must contain attempt")
	}
	var eventAttempt int
	if err := strictDecodeJSONV3(eventPayload["attempt"], &eventAttempt); err != nil || eventAttempt != value.LeaseAttempt {
		return fmt.Errorf("Quality completion event attempt differs from the lease")
	}
	if len(value.GateInputCanonical) == 0 || len(value.GateInputCanonical) > maximumQualityResultV3Bytes ||
		!bytes.Equal(bytes.TrimSpace(value.GateInputCanonical), value.GateInputCanonical) ||
		rejectDuplicateJSONNamesV3(value.GateInputCanonical) != nil {
		return fmt.Errorf("Quality completion gate input is not bounded canonical JSON")
	}
	if err := requireExactObjectFieldsV3(value.GateInputCanonical, []string{"bindings", "hash"}, nil); err != nil {
		return fmt.Errorf("Quality completion gate input: %w", err)
	}
	var envelope domain.NodeInputEnvelope
	if err := json.Unmarshal(value.GateInputCanonical, &envelope); err != nil || envelope.Validate() != nil ||
		!bytes.Equal(envelope.Canonical(), value.GateInputCanonical) {
		return fmt.Errorf("Quality completion gate input is not the exact canonical NodeInput envelope")
	}
	bindings := envelope.Bindings()
	if len(bindings) != 1 || value.GateInputBindingCount != 1 || len(bindings[0].Mapping) != 0 ||
		bindings[0].Source.RunID != value.WorkflowRunID || bindings[0].Source.NodeKey != value.QualityNodeKey ||
		bindings[0].Source.OutputRevisionID != value.OutputRevisionID ||
		bindings[0].FromPort != "default" || bindings[0].ToPort != "default" ||
		!bytes.Equal(bindings[0].Output, bindings[0].Value) {
		return fmt.Errorf("Quality completion gate input is not one exact identity-mapped Quality binding")
	}
	if value.GateInputRawSize != int64(len(value.GateInputCanonical)) ||
		value.GateInputRawHash != qualityCompletionRawSHA256(value.GateInputCanonical) ||
		value.GateInputSemanticHash != normalizeQualityCompletionSHA256(envelope.Hash()) {
		return fmt.Errorf("Quality completion gate input byte or semantic digest differs")
	}
	return nil
}

func validateQualityCompletionRunMutation(mutation RunMutation) error {
	precommit := mutation.QualityCompletionPrecommit
	if precommit == nil {
		return nil
	}
	if err := precommit.Validate(); err != nil {
		return err
	}
	if mutation.RunID != precommit.WorkflowRunID || mutation.ExpectedCursor != precommit.ExpectedRunCursor ||
		!mutation.UpdatedAt.Equal(precommit.CompletedAt) ||
		len(mutation.Events) != 1 {
		return fmt.Errorf("Quality completion precommit does not match its run mutation")
	}
	event := mutation.Events[0]
	if event.ID != precommit.CompletionEventID || event.RunID != mutation.RunID || event.Sequence != precommit.CompletionEventSequence ||
		event.Type != "node.completed" || event.NodeKey != precommit.QualityNodeKey || event.ActorID != precommit.CompletionEventActorID ||
		!event.CreatedAt.Equal(precommit.CompletedAt) ||
		!canonicalQualityCompletionJSONEqual(event.Payload, precommit.CompletionEventPayload) {
		return fmt.Errorf("Quality completion event does not match its precommit")
	}
	qualityCount := 0
	for _, nodeMutation := range mutation.Nodes {
		if nodeMutation.Node.ID != precommit.QualityNodeRunID {
			continue
		}
		qualityCount++
		node := nodeMutation.Node
		if node.Key != precommit.QualityNodeKey || node.RunID != mutation.RunID || node.Type != domain.NodeQualityGate ||
			nodeMutation.ExpectedStatus != NodeRunning || nodeMutation.ExpectedOwner != precommit.LeaseOwner ||
			node.Status != NodeCompleted || node.Attempt != precommit.LeaseAttempt || node.OutputRevisionID != precommit.OutputRevisionID ||
			node.CompletedAt == nil || !node.CompletedAt.Equal(precommit.CompletedAt) ||
			node.LeaseOwner != "" || node.LeaseExpiresAt != nil || len(node.Failure) != 0 {
			return fmt.Errorf("Quality completion node mutation does not match its precommit")
		}
	}
	if qualityCount != 1 {
		return fmt.Errorf("Quality completion precommit requires one Quality node mutation")
	}
	gateMetadata, exists := mutation.Context.Nodes[precommit.GateNodeKey]
	if !exists || !bytes.Equal(gateMetadata.Input, precommit.GateInputCanonical) || len(gateMetadata.Output) != 0 {
		return fmt.Errorf("Quality completion gate context does not match its precommit")
	}
	bindings := qualityCompletionBindings(precommit.GateInputCanonical)
	qualityMetadata, exists := mutation.Context.Nodes[precommit.QualityNodeKey]
	if !exists || len(bindings) != 1 || !bytes.Equal(qualityMetadata.Output, bindings[0].Output) {
		return fmt.Errorf("Quality completion output does not match its gate binding")
	}
	return nil
}

func qualityCompletionActivationIdentities(value *QualityCompletionPrecommitMutation) []string {
	if value == nil {
		return nil
	}
	return []string{
		value.WorkflowInputOperationID,
		value.WorkflowInputAuthorityID,
		value.ActivationEventID,
	}
}

func qualityCompletionBindings(raw json.RawMessage) []domain.NodeInputBinding {
	var envelope domain.NodeInputEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	return envelope.Bindings()
}

func sameQualityCompletionPrecommit(left, right *QualityCompletionPrecommitMutation) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftClone, rightClone := cloneQualityCompletionPrecommitMutation(left), cloneQualityCompletionPrecommitMutation(right)
	leftClone.CompletionEventPayload = nil
	rightClone.CompletionEventPayload = nil
	leftClone.GateInputCanonical = nil
	rightClone.GateInputCanonical = nil
	return reflect.DeepEqual(leftClone, rightClone) &&
		bytes.Equal(left.CompletionEventPayload, right.CompletionEventPayload) &&
		bytes.Equal(left.GateInputCanonical, right.GateInputCanonical)
}

func qualityCompletionUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 4 && parsed.String() == value
}

func qualityCompletionRawSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func normalizeQualityCompletionSHA256(value string) string {
	if strings.HasPrefix(value, "sha256:") {
		return value
	}
	return "sha256:" + value
}

func canonicalQualityCompletionJSONEqual(left, right json.RawMessage) bool {
	leftCanonical, leftErr := domain.CanonicalJSON(left)
	rightCanonical, rightErr := domain.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}
