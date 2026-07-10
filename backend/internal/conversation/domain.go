package conversation

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
)

type ConversationStatus string

const (
	ConversationActive   ConversationStatus = "active"
	ConversationArchived ConversationStatus = "archived"
)

type MessageRole string

const (
	MessageUser      MessageRole = "user"
	MessageAssistant MessageRole = "assistant"
)

type IntentKind string

const (
	IntentStartWorkflow        IntentKind = "start_workflow"
	IntentWorkbenchInstruction IntentKind = "workbench_instruction"
)

type ProposalStatus string

const (
	ProposalPending  ProposalStatus = "pending"
	ProposalAccepted ProposalStatus = "accepted"
	ProposalRejected ProposalStatus = "rejected"
)

type ProposalOrigin string

const (
	ProposalOriginSubmitted ProposalOrigin = "submitted"
	ProposalOriginAI        ProposalOrigin = "ai"
)

type ProposalDecision string

const (
	DecisionAccept ProposalDecision = "accept"
	DecisionReject ProposalDecision = "reject"
)

type CommandStatus string

const (
	CommandPending  CommandStatus = "pending"
	CommandExecuted CommandStatus = "executed"
	CommandRejected CommandStatus = "rejected"
	CommandFailed   CommandStatus = "failed"
)

type Conversation struct {
	ID         string             `json:"id"`
	ProjectID  string             `json:"projectId"`
	Title      string             `json:"title"`
	Status     ConversationStatus `json:"status"`
	Version    uint64             `json:"version"`
	ETag       string             `json:"etag"`
	CreatedBy  string             `json:"createdBy"`
	CreatedAt  time.Time          `json:"createdAt"`
	UpdatedAt  time.Time          `json:"updatedAt"`
	ArchivedAt *time.Time         `json:"archivedAt,omitempty"`
}

type Message struct {
	ID             string      `json:"id"`
	ConversationID string      `json:"conversationId"`
	Sequence       uint64      `json:"sequence"`
	Role           MessageRole `json:"role"`
	Content        string      `json:"content"`
	ProposalID     *string     `json:"proposalId,omitempty"`
	CreatedBy      string      `json:"createdBy"`
	CreatedAt      time.Time   `json:"createdAt"`
}

type ManifestIntent struct {
	Mode          string                     `json:"mode"`
	InputManifest platformdomain.ManifestRef `json:"inputManifest"`
	Purpose       string                     `json:"purpose"`
}

type WorkbenchInstruction struct {
	Objective        string   `json:"objective"`
	Constraints      []string `json:"constraints,omitempty"`
	ExpectedRunID    string   `json:"expectedRunId,omitempty"`
	ExpectedBundleID string   `json:"expectedBundleId,omitempty"`
}

// ReviewedConversationIntent is the immutable, server-identified intent that
// is copied into workflow run scope after a human accepts the proposal. Its
// IDs are minted by the proposal transaction and cannot be supplied by the
// client or model.
type ReviewedConversationIntent struct {
	ConversationID       string                       `json:"conversationId"`
	TriggerMessageID     string                       `json:"triggerMessageId"`
	ProposalID           string                       `json:"proposalId"`
	AssistantMessageID   string                       `json:"assistantMessageId"`
	Kind                 IntentKind                   `json:"kind"`
	DefinitionVersionID  string                       `json:"definitionVersionId"`
	WorkbenchInstruction WorkbenchInstruction         `json:"workbenchInstruction"`
	ManifestIntent       ManifestIntent               `json:"manifestIntent"`
	SourceRefs           []platformdomain.ArtifactRef `json:"sourceRefs"`
}

type AIProvenance struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	ResponseID string `json:"responseId,omitempty"`
}

type ProposalProvenance struct {
	Origin ProposalOrigin
	AI     *AIProvenance
}

type WorkflowIntentProposal struct {
	ID                           string                       `json:"id"`
	ProjectID                    string                       `json:"projectId"`
	ConversationID               string                       `json:"conversationId"`
	TriggerMessageID             string                       `json:"triggerMessageId"`
	AssistantMessageID           string                       `json:"assistantMessageId"`
	Kind                         IntentKind                   `json:"kind"`
	Status                       ProposalStatus               `json:"status"`
	Version                      uint64                       `json:"version"`
	ETag                         string                       `json:"etag"`
	SuggestedDefinitionVersionID string                       `json:"suggestedDefinitionVersionId"`
	Scope                        json.RawMessage              `json:"scope"`
	SourceRefs                   []platformdomain.ArtifactRef `json:"sourceRefs"`
	ManifestIntent               ManifestIntent               `json:"manifestIntent"`
	WorkbenchInstruction         WorkbenchInstruction         `json:"workbenchInstruction"`
	Origin                       ProposalOrigin               `json:"origin"`
	AI                           *AIProvenance                `json:"ai,omitempty"`
	DecisionReason               string                       `json:"decisionReason,omitempty"`
	ProposedBy                   string                       `json:"proposedBy"`
	DecidedBy                    *string                      `json:"decidedBy,omitempty"`
	CreatedAt                    time.Time                    `json:"createdAt"`
	DecidedAt                    *time.Time                   `json:"decidedAt,omitempty"`
}

// CommandPayload is the immutable snapshot made when a human accepts an
// intent proposal. Executors never re-read mutable conversation text.
type CommandPayload struct {
	DefinitionVersionID string                       `json:"definitionVersionId"`
	Scope               json.RawMessage              `json:"scope"`
	SourceRefs          []platformdomain.ArtifactRef `json:"sourceRefs"`
	ManifestIntent      ManifestIntent               `json:"manifestIntent"`
	Workbench           WorkbenchInstruction         `json:"workbench"`
}

type CommandFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ConversationCommand struct {
	ID               string          `json:"id"`
	ProjectID        string          `json:"projectId"`
	ConversationID   string          `json:"conversationId"`
	ProposalID       string          `json:"proposalId"`
	Kind             IntentKind      `json:"kind"`
	Status           CommandStatus   `json:"status"`
	Version          uint64          `json:"version"`
	ETag             string          `json:"etag"`
	Payload          CommandPayload  `json:"payload"`
	Result           json.RawMessage `json:"result,omitempty"`
	Failure          *CommandFailure `json:"failure,omitempty"`
	AcceptedBy       string          `json:"acceptedBy"`
	ExecutionActorID *string         `json:"executionActorId,omitempty"`
	ExecutedBy       *string         `json:"executedBy,omitempty"`
	RejectedBy       *string         `json:"rejectedBy,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
	ExecutedAt       *time.Time      `json:"executedAt,omitempty"`
	RejectedAt       *time.Time      `json:"rejectedAt,omitempty"`
	FailedAt         *time.Time      `json:"failedAt,omitempty"`
}

type CreateConversationInput struct {
	Title string `json:"title"`
}

type UpdateConversationInput struct {
	Title  *string             `json:"title,omitempty"`
	Status *ConversationStatus `json:"status,omitempty"`
}

type AppendMessageInput struct {
	Content string `json:"content"`
}

type CreateIntentProposalInput struct {
	TriggerMessageID             string                       `json:"triggerMessageId"`
	AssistantContent             string                       `json:"assistantContent"`
	Kind                         IntentKind                   `json:"kind"`
	SuggestedDefinitionVersionID string                       `json:"suggestedDefinitionVersionId"`
	Scope                        json.RawMessage              `json:"scope"`
	SourceRefs                   []platformdomain.ArtifactRef `json:"sourceRefs"`
	ManifestIntent               ManifestIntent               `json:"manifestIntent"`
	WorkbenchInstruction         WorkbenchInstruction         `json:"workbenchInstruction"`
}

type GenerateIntentProposalInput struct {
	TriggerMessageID              string                       `json:"triggerMessageId"`
	CandidateDefinitionVersionIDs []string                     `json:"candidateDefinitionVersionIds"`
	SourceRefs                    []platformdomain.ArtifactRef `json:"sourceRefs"`
	ManifestIntent                ManifestIntent               `json:"manifestIntent"`
	Model                         string                       `json:"model,omitempty"`
}

type GeneratedIntentProposal struct {
	Proposal WorkflowIntentProposal `json:"proposal"`
	Message  Message                `json:"message"`
	Provider string                 `json:"provider"`
	Model    string                 `json:"model"`
}

type DecideProposalInput struct {
	Decision ProposalDecision `json:"decision"`
	Reason   string           `json:"reason,omitempty"`
}

type WorkbenchExecutionResult struct {
	RunID                    string `json:"runId"`
	BundleID                 string `json:"bundleId"`
	ImplementationProposalID string `json:"implementationProposalId"`
}

type ExecuteCommandInput struct {
	WorkbenchResult *WorkbenchExecutionResult `json:"workbenchResult,omitempty"`
}

type RejectCommandInput struct {
	Reason string `json:"reason"`
}

type ListOptions struct {
	Limit  int
	Cursor string
}

type ConversationPage struct {
	Items      []Conversation `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type MessagePage struct {
	Items      []Message `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

type ProposalPage struct {
	Items      []WorkflowIntentProposal `json:"items"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}

type CommandPage struct {
	Items      []ConversationCommand `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func ConversationETag(id string, version uint64) string {
	return fmt.Sprintf(`"conversation:%s:%d"`, id, version)
}

func ProposalETag(id string, version uint64) string {
	return fmt.Sprintf(`"workflow-intent-proposal:%s:%d"`, id, version)
}

func CommandETag(id string, version uint64) string {
	return fmt.Sprintf(`"conversation-command:%s:%d"`, id, version)
}

func normalizeListOptions(options ListOptions) (ListOptions, error) {
	if options.Limit == 0 {
		options.Limit = 50
	}
	if options.Limit < 1 || options.Limit > 200 {
		return ListOptions{}, fmt.Errorf("%w: list limit", core.ErrInvalidInput)
	}
	if len(options.Cursor) > 2048 {
		return ListOptions{}, fmt.Errorf("%w: list cursor", core.ErrInvalidInput)
	}
	return options, nil
}

type historyCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func encodeHistoryCursor(createdAt time.Time, id uuid.UUID) string {
	payload, _ := json.Marshal(historyCursor{CreatedAt: createdAt.UTC(), ID: id.String()})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeHistoryCursor(value string) (time.Time, uuid.UUID, error) {
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: cursor", core.ErrInvalidInput)
	}
	var cursor historyCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.CreatedAt.IsZero() {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: cursor", core.ErrInvalidInput)
	}
	id, err := uuid.Parse(cursor.ID)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: cursor", core.ErrInvalidInput)
	}
	return cursor.CreatedAt.UTC(), id, nil
}

func encodeMessageCursor(sequence uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("sequence:%d", sequence)))
}

func decodeMessageCursor(value string) (uint64, error) {
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, fmt.Errorf("%w: cursor", core.ErrInvalidInput)
	}
	encoded := string(payload)
	if !strings.HasPrefix(encoded, "sequence:") {
		return 0, fmt.Errorf("%w: cursor", core.ErrInvalidInput)
	}
	sequence, err := strconv.ParseUint(strings.TrimPrefix(encoded, "sequence:"), 10, 64)
	if err != nil || sequence == 0 {
		return 0, fmt.Errorf("%w: cursor", core.ErrInvalidInput)
	}
	return sequence, nil
}

func normalizeContent(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 32768 {
		return "", fmt.Errorf("%w: message content", core.ErrInvalidInput)
	}
	return value, nil
}

func normalizeTitle(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 160 {
		return "", fmt.Errorf("%w: conversation title", core.ErrInvalidInput)
	}
	return value, nil
}

func normalizeProposalInput(input CreateIntentProposalInput) (CreateIntentProposalInput, error) {
	if _, err := uuid.Parse(input.TriggerMessageID); err != nil {
		return input, fmt.Errorf("%w: trigger message id", core.ErrInvalidInput)
	}
	if _, err := uuid.Parse(input.SuggestedDefinitionVersionID); err != nil {
		return input, fmt.Errorf("%w: definition version id", core.ErrInvalidInput)
	}
	content, err := normalizeContent(input.AssistantContent)
	if err != nil {
		return input, err
	}
	input.AssistantContent = content
	if input.Kind != IntentStartWorkflow && input.Kind != IntentWorkbenchInstruction {
		return input, fmt.Errorf("%w: intent kind", core.ErrInvalidInput)
	}
	if len(input.Scope) == 0 {
		input.Scope = json.RawMessage(`{}`)
	}
	canonicalScope, err := platformdomain.CanonicalJSON(input.Scope)
	if err != nil || len(canonicalScope) > 65536 {
		return input, fmt.Errorf("%w: workflow scope", core.ErrInvalidInput)
	}
	var scope map[string]any
	if err := json.Unmarshal(canonicalScope, &scope); err != nil || scope == nil {
		return input, fmt.Errorf("%w: workflow scope must be an object", core.ErrInvalidInput)
	}
	if _, reserved := scope["conversationIntent"]; reserved {
		return input, fmt.Errorf("%w: scope.conversationIntent is server managed", core.ErrInvalidInput)
	}
	if len(input.SourceRefs) == 0 || len(input.SourceRefs) > 256 {
		return input, fmt.Errorf("%w: exact source refs", core.ErrInvalidInput)
	}
	seen := make(map[string]struct{}, len(input.SourceRefs))
	for index, ref := range input.SourceRefs {
		if err := ref.Validate(); err != nil {
			return input, fmt.Errorf("%w: sourceRefs[%d]", core.ErrInvalidInput, index)
		}
		if _, err := uuid.Parse(ref.ArtifactID); err != nil {
			return input, fmt.Errorf("%w: sourceRefs[%d].artifactId", core.ErrInvalidInput, index)
		}
		if _, err := uuid.Parse(ref.RevisionID); err != nil {
			return input, fmt.Errorf("%w: sourceRefs[%d].revisionId", core.ErrInvalidInput, index)
		}
		if len(ref.AnchorID) > 512 {
			return input, fmt.Errorf("%w: sourceRefs[%d].anchorId", core.ErrInvalidInput, index)
		}
		key := ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" + ref.AnchorID
		if _, duplicate := seen[key]; duplicate {
			return input, fmt.Errorf("%w: duplicate source ref", core.ErrInvalidInput)
		}
		seen[key] = struct{}{}
	}
	input.ManifestIntent.Mode = strings.TrimSpace(input.ManifestIntent.Mode)
	input.ManifestIntent.Purpose = strings.TrimSpace(input.ManifestIntent.Purpose)
	if input.ManifestIntent.Mode != "use_existing" || input.ManifestIntent.Purpose == "" || len(input.ManifestIntent.Purpose) > 512 {
		return input, fmt.Errorf("%w: immutable manifest intent", core.ErrInvalidInput)
	}
	if err := input.ManifestIntent.InputManifest.Validate(); err != nil {
		return input, fmt.Errorf("%w: input manifest", core.ErrInvalidInput)
	}
	if _, err := uuid.Parse(input.ManifestIntent.InputManifest.ID); err != nil {
		return input, fmt.Errorf("%w: input manifest id", core.ErrInvalidInput)
	}
	input.WorkbenchInstruction.Objective = strings.TrimSpace(input.WorkbenchInstruction.Objective)
	if input.WorkbenchInstruction.Objective == "" || len(input.WorkbenchInstruction.Objective) > 4000 || len(input.WorkbenchInstruction.Constraints) > 100 {
		return input, fmt.Errorf("%w: workbench instruction", core.ErrInvalidInput)
	}
	for index, constraint := range input.WorkbenchInstruction.Constraints {
		constraint = strings.TrimSpace(constraint)
		if constraint == "" || len(constraint) > 1000 {
			return input, fmt.Errorf("%w: workbench constraint %d", core.ErrInvalidInput, index)
		}
		input.WorkbenchInstruction.Constraints[index] = constraint
	}
	for _, id := range []string{input.WorkbenchInstruction.ExpectedRunID, input.WorkbenchInstruction.ExpectedBundleID} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return input, fmt.Errorf("%w: workbench expected identity", core.ErrInvalidInput)
			}
		}
	}
	if input.Kind == IntentWorkbenchInstruction && (input.WorkbenchInstruction.ExpectedRunID == "" || input.WorkbenchInstruction.ExpectedBundleID == "") {
		return input, fmt.Errorf("%w: workbench instruction requires expectedRunId and expectedBundleId", core.ErrInvalidInput)
	}
	// The reviewed conversationIntent envelope is added only after the store
	// mints the proposal and assistant-message IDs. Keeping it absent here makes
	// it impossible for a client or model to preselect those server identities.
	input.Scope = canonicalScope
	return input, nil
}
