package conversation

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
)

const (
	ConversationPrefixHashAlgorithm = "conversation-prefix-chain/v1"
	maxSummaryCheckpointBytes       = 32 << 10
	maxSummaryReviewReasonBytes     = 4000
)

var ErrSummaryCheckpointChainStale = fmt.Errorf("%w: conversation summary checkpoint chain changed", core.ErrConflict)

type SummaryCheckpointStatus string

const (
	SummaryCheckpointPendingReview SummaryCheckpointStatus = "pending_review"
	SummaryCheckpointApproved      SummaryCheckpointStatus = "approved"
	SummaryCheckpointRejected      SummaryCheckpointStatus = "rejected"
	SummaryCheckpointSuperseded    SummaryCheckpointStatus = "superseded"
)

type SummaryCheckpointDecision string

const (
	SummaryCheckpointApprove SummaryCheckpointDecision = "approve"
	SummaryCheckpointReject  SummaryCheckpointDecision = "reject"
)

type ConversationSummaryCheckpoint struct {
	ID                   string                  `json:"id"`
	ProjectID            string                  `json:"projectId"`
	ConversationID       string                  `json:"conversationId"`
	PreviousCheckpointID *string                 `json:"previousCheckpointId,omitempty"`
	ThroughMessageID     string                  `json:"throughMessageId"`
	ThroughSequence      uint64                  `json:"throughSequence"`
	MessageCount         uint64                  `json:"messageCount"`
	ContentBytes         uint64                  `json:"contentBytes"`
	PrefixHash           string                  `json:"prefixHash"`
	HashAlgorithm        string                  `json:"hashAlgorithm"`
	Summary              string                  `json:"summary"`
	SummaryHash          string                  `json:"summaryHash"`
	Status               SummaryCheckpointStatus `json:"status"`
	Version              uint64                  `json:"version"`
	ETag                 string                  `json:"etag"`
	CreatedBy            string                  `json:"createdBy"`
	CreatedAt            time.Time               `json:"createdAt"`
	ReviewedBy           *string                 `json:"reviewedBy,omitempty"`
	ReviewedAt           *time.Time              `json:"reviewedAt,omitempty"`
	ReviewReason         string                  `json:"reviewReason,omitempty"`
}

type CreateSummaryCheckpointInput struct {
	ThroughMessageID string `json:"throughMessageId"`
	Summary          string `json:"summary"`
}

type DecideSummaryCheckpointInput struct {
	Decision SummaryCheckpointDecision `json:"decision"`
	Reason   string                    `json:"reason,omitempty"`
}

type SummaryCheckpointPage struct {
	Items      []ConversationSummaryCheckpoint `json:"items"`
	NextCursor string                          `json:"nextCursor,omitempty"`
}

type ApprovedSummaryCheckpointRef struct {
	ID               string `json:"id"`
	ThroughMessageID string `json:"throughMessageId"`
	ThroughSequence  uint64 `json:"throughSequence"`
	PrefixHash       string `json:"prefixHash"`
	SummaryHash      string `json:"summaryHash"`
	Summary          string `json:"summary"`
}

type ConversationTailProvenance struct {
	FromSequence uint64 `json:"fromSequence"`
	ToSequence   uint64 `json:"toSequence"`
	MessageCount uint64 `json:"messageCount"`
	ContentBytes uint64 `json:"contentBytes"`
	Hash         string `json:"hash"`
}

type ConversationContextProvenance struct {
	Version           int                           `json:"version"`
	Mode              string                        `json:"mode"`
	TriggerMessageID  string                        `json:"triggerMessageId"`
	Checkpoint        *ApprovedSummaryCheckpointRef `json:"checkpoint,omitempty"`
	Tail              ConversationTailProvenance    `json:"tail"`
	ContextHash       string                        `json:"contextHash"`
	ProviderInputHash string                        `json:"providerInputHash,omitempty"`
}

type ProviderConversationContext struct {
	ApprovedCheckpoint *ApprovedSummaryCheckpointRef `json:"approvedCheckpoint"`
	TailMessages       []Message                     `json:"tailMessages"`
}

type IntentSummaryCheckpointRequiredError struct {
	TriggerMessageID            string
	TriggerSequence             uint64
	MessageCount                uint64
	MessageContentBytes         uint64
	ContextBytes                uint64
	CurrentApprovedCheckpointID string
	CurrentThroughSequence      uint64
	RecommendedThroughMessageID string
	RecommendedThroughSequence  uint64
}

func (e *IntentSummaryCheckpointRequiredError) Error() string {
	return fmt.Sprintf(
		"%v: checkpoint plus tail through trigger %s has %d logical messages and %d bytes; limits are %d messages and %d bytes",
		ErrIntentSummaryCheckpointRequired,
		e.TriggerMessageID,
		e.MessageCount,
		e.ContextBytes,
		maxIntentConversationMessages,
		maxIntentConversationMessageBytes,
	)
}

func (e *IntentSummaryCheckpointRequiredError) Unwrap() error {
	return ErrIntentSummaryCheckpointRequired
}

func SummaryCheckpointETag(id string, version uint64) string {
	return fmt.Sprintf(`"conversation-summary-checkpoint:%s:%d"`, id, version)
}

func normalizeSummaryCheckpointInput(input CreateSummaryCheckpointInput) (CreateSummaryCheckpointInput, error) {
	throughID := strings.TrimSpace(input.ThroughMessageID)
	parsed, err := uuid.Parse(throughID)
	if err != nil || parsed.String() != throughID {
		return input, fmt.Errorf("%w: checkpoint through message id", core.ErrInvalidInput)
	}
	input.ThroughMessageID = throughID
	input.Summary = strings.TrimSpace(input.Summary)
	if input.Summary == "" || len(input.Summary) > maxSummaryCheckpointBytes {
		return input, fmt.Errorf("%w: checkpoint summary", core.ErrInvalidInput)
	}
	return input, nil
}

func normalizeSummaryCheckpointDecision(input DecideSummaryCheckpointInput) (DecideSummaryCheckpointInput, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Decision != SummaryCheckpointApprove && input.Decision != SummaryCheckpointReject {
		return input, fmt.Errorf("%w: checkpoint review decision", core.ErrInvalidInput)
	}
	if len(input.Reason) > maxSummaryReviewReasonBytes || input.Decision == SummaryCheckpointReject && input.Reason == "" {
		return input, fmt.Errorf("%w: checkpoint review reason", core.ErrInvalidInput)
	}
	return input, nil
}

func conversationPrefixGenesis(conversationID uuid.UUID) []byte {
	payload := append([]byte("worksflow/conversation-prefix-chain/v1\x00"), conversationID[:]...)
	digest := sha256.Sum256(payload)
	return append([]byte(nil), digest[:]...)
}

func advanceConversationPrefixHash(previous []byte, message Message) ([]byte, error) {
	if len(previous) != sha256.Size {
		return nil, fmt.Errorf("invalid conversation prefix hash")
	}
	canonical, err := platformdomain.CanonicalJSON(struct {
		ID             string      `json:"id"`
		ConversationID string      `json:"conversationId"`
		Sequence       uint64      `json:"sequence"`
		Role           MessageRole `json:"role"`
		Content        string      `json:"content"`
		ProposalID     *string     `json:"proposalId,omitempty"`
		CreatedBy      string      `json:"createdBy"`
		CreatedAt      time.Time   `json:"createdAt"`
	}{
		ID: message.ID, ConversationID: message.ConversationID, Sequence: message.Sequence,
		Role: message.Role, Content: message.Content, ProposalID: message.ProposalID,
		CreatedBy: message.CreatedBy, CreatedAt: message.CreatedAt.UTC(),
	})
	if err != nil {
		return nil, err
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("worksflow/conversation-prefix-step/v1\x00"))
	_, _ = hasher.Write(previous)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write(canonical)
	return hasher.Sum(nil), nil
}

func sha256Bytes(value []byte) []byte {
	digest := sha256.Sum256(value)
	return append([]byte(nil), digest[:]...)
}

func sha256Ref(value []byte) string {
	return "sha256:" + hex.EncodeToString(value)
}

func parseSHA256Ref(value string) ([]byte, error) {
	if !strings.HasPrefix(value, "sha256:") {
		return nil, fmt.Errorf("invalid sha256 reference")
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	if err != nil || len(decoded) != sha256.Size {
		return nil, fmt.Errorf("invalid sha256 reference")
	}
	return decoded, nil
}
