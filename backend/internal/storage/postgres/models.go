package postgres

import (
	"encoding/json"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// Models deliberately keep relationships as identifiers. Cross-aggregate loading is
// explicit in repositories so a GORM preload cannot accidentally cross a project
// authorization boundary.

type UserModel struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	Email        string    `gorm:"not null"`
	DisplayName  string    `gorm:"not null"`
	PasswordHash string    `gorm:"not null"`
	AvatarURL    *string
	DisabledAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (UserModel) TableName() string { return "users" }

type AuthSessionModel struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index"`
	TokenHash  []byte    `gorm:"not null;uniqueIndex"`
	ExpiresAt  time.Time `gorm:"not null;index"`
	RevokedAt  *time.Time
	LastSeenAt time.Time
	UserAgent  *string
	IPAddress  *netip.Addr `gorm:"type:inet"`
	CreatedAt  time.Time
}

func (AuthSessionModel) TableName() string { return "auth_sessions" }

type ProjectModel struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey"`
	Slug           *string
	Name           string    `gorm:"not null"`
	Description    string    `gorm:"not null"`
	Lifecycle      string    `gorm:"not null"`
	GovernanceMode string    `gorm:"not null;default:team"`
	Version        uint64    `gorm:"not null"`
	CreatedBy      uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     *time.Time
}

func (ProjectModel) TableName() string { return "projects" }

type ProjectMemberModel struct {
	ProjectID uuid.UUID  `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"type:uuid;primaryKey"`
	Role      string     `gorm:"not null"`
	InvitedBy *uuid.UUID `gorm:"type:uuid"`
	JoinedAt  time.Time
	UpdatedAt time.Time
}

func (ProjectMemberModel) TableName() string { return "project_members" }

type ProjectInvitationModel struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID  uuid.UUID  `gorm:"type:uuid;not null;index"`
	Email      string     `gorm:"not null"`
	Role       string     `gorm:"not null"`
	TokenHash  []byte     `gorm:"not null;uniqueIndex"`
	Status     string     `gorm:"not null"`
	InvitedBy  uuid.UUID  `gorm:"type:uuid;not null"`
	AcceptedBy *uuid.UUID `gorm:"type:uuid"`
	ExpiresAt  time.Time  `gorm:"not null"`
	CreatedAt  time.Time
	AcceptedAt *time.Time
	RevokedAt  *time.Time
}

func (ProjectInvitationModel) TableName() string { return "project_invitations" }

type ArtifactModel struct {
	ID                       uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID                uuid.UUID  `gorm:"type:uuid;not null;index"`
	Kind                     string     `gorm:"not null"`
	ArtifactKey              string     `gorm:"not null"`
	Title                    string     `gorm:"not null"`
	Lifecycle                string     `gorm:"not null"`
	Version                  uint64     `gorm:"not null"`
	LatestDraftID            *uuid.UUID `gorm:"type:uuid"`
	LatestRevisionID         *uuid.UUID `gorm:"type:uuid"`
	LatestApprovedRevisionID *uuid.UUID `gorm:"type:uuid"`
	CreatedBy                uuid.UUID  `gorm:"type:uuid;not null"`
	CreatedAt                time.Time
	UpdatedAt                time.Time
	ArchivedAt               *time.Time
}

func (ArtifactModel) TableName() string { return "artifacts" }

type ArtifactDraftModel struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ArtifactID     uuid.UUID  `gorm:"type:uuid;not null;index"`
	BaseRevisionID *uuid.UUID `gorm:"type:uuid"`
	Sequence       uint64     `gorm:"not null"`
	ETag           string     `gorm:"column:etag;not null"`
	SchemaVersion  int        `gorm:"not null"`
	ContentStore   string     `gorm:"not null"`
	ContentRef     string     `gorm:"not null"`
	ContentHash    string     `gorm:"not null"`
	ByteSize       int64      `gorm:"not null"`
	Status         string     `gorm:"not null"`
	CreatedBy      uuid.UUID  `gorm:"type:uuid;not null"`
	UpdatedBy      uuid.UUID  `gorm:"type:uuid;not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (ArtifactDraftModel) TableName() string { return "artifact_drafts" }

type ArtifactDraftSourceModel struct {
	DraftID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	SourceArtifactID  uuid.UUID `gorm:"type:uuid;not null;index"`
	SourceRevisionID  uuid.UUID `gorm:"type:uuid;primaryKey"`
	SourceContentHash string    `gorm:"not null"`
	SourceAnchorID    *string
	Purpose           string    `gorm:"primaryKey"`
	Required          bool      `gorm:"not null"`
	AddedBy           uuid.UUID `gorm:"type:uuid;not null"`
	AddedAt           time.Time
}

func (ArtifactDraftSourceModel) TableName() string { return "artifact_draft_sources" }

type ArtifactRevisionModel struct {
	ID                       uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ArtifactID               uuid.UUID  `gorm:"type:uuid;not null;index"`
	RevisionNumber           uint64     `gorm:"not null"`
	ParentRevisionID         *uuid.UUID `gorm:"type:uuid"`
	SchemaVersion            int        `gorm:"not null"`
	ContentStore             string     `gorm:"not null"`
	ContentRef               string     `gorm:"not null"`
	ContentHash              string     `gorm:"not null"`
	ByteSize                 int64      `gorm:"not null"`
	WorkflowStatus           string     `gorm:"not null"`
	ChangeSource             string     `gorm:"not null"`
	ChangeSummary            string     `gorm:"not null"`
	SourceManifestID         *uuid.UUID `gorm:"type:uuid"`
	ProposalID               *uuid.UUID `gorm:"type:uuid"`
	ImplementationProposalID *uuid.UUID `gorm:"type:uuid"`
	CreatedBy                uuid.UUID  `gorm:"type:uuid;not null"`
	CreatedAt                time.Time
	ApprovedAt               *time.Time
	SupersededAt             *time.Time
}

func (ArtifactRevisionModel) TableName() string { return "artifact_revisions" }

type ArtifactRevisionSourceModel struct {
	RevisionID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	Ordinal           int       `gorm:"not null"`
	SourceArtifactID  uuid.UUID `gorm:"type:uuid;not null;index"`
	SourceRevisionID  uuid.UUID `gorm:"type:uuid;primaryKey"`
	SourceContentHash string    `gorm:"not null"`
	SourceAnchorID    *string
	Purpose           string    `gorm:"primaryKey"`
	Required          bool      `gorm:"not null"`
	AddedBy           uuid.UUID `gorm:"type:uuid;not null"`
	AddedAt           time.Time
}

func (ArtifactRevisionSourceModel) TableName() string { return "artifact_revision_sources" }

type ArtifactResponsibilityModel struct {
	ArtifactID     uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	Responsibility string    `gorm:"primaryKey"`
	Reason         string    `gorm:"not null"`
	AssignedBy     uuid.UUID `gorm:"type:uuid;not null"`
	AssignedAt     time.Time
}

func (ArtifactResponsibilityModel) TableName() string { return "artifact_responsibilities" }

type ArtifactCollaborationStateModel struct {
	ArtifactID uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID  uuid.UUID `gorm:"type:uuid;not null;index"`
	Version    uint64    `gorm:"not null"`
	UpdatedBy  uuid.UUID `gorm:"type:uuid;not null"`
	UpdatedAt  time.Time
}

func (ArtifactCollaborationStateModel) TableName() string { return "artifact_collaboration_states" }

type ArtifactMemberBindingModel struct {
	ArtifactID uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID  uuid.UUID `gorm:"type:uuid;not null;index"`
	UserID     uuid.UUID `gorm:"type:uuid;primaryKey"`
	Role       string    `gorm:"primaryKey"`
	Reason     string    `gorm:"not null"`
	AssignedBy uuid.UUID `gorm:"type:uuid;not null"`
	AssignedAt time.Time
}

func (ArtifactMemberBindingModel) TableName() string { return "artifact_member_bindings" }

type DocumentGenerationCommandModel struct {
	ID                 uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID          uuid.UUID       `gorm:"type:uuid;not null;index"`
	ActorID            uuid.UUID       `gorm:"type:uuid;not null"`
	CommandKey         string          `gorm:"not null"`
	RequestHash        string          `gorm:"not null"`
	SourceBindingsETag string          `gorm:"column:source_bindings_etag;not null"`
	ResolvedOwnerIDs   json.RawMessage `gorm:"type:jsonb;not null"`
	Status             string          `gorm:"not null"`
	TargetArtifactID   *uuid.UUID      `gorm:"type:uuid"`
	BaseRevisionID     *uuid.UUID      `gorm:"type:uuid"`
	InputManifestID    *uuid.UUID      `gorm:"type:uuid"`
	OutputProposalID   *uuid.UUID      `gorm:"type:uuid"`
	Provider           string          `gorm:"not null"`
	Model              string          `gorm:"not null"`
	AttemptCount       int             `gorm:"not null"`
	LastFailure        *string
	LastFailedAt       *time.Time
	LockedUntil        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (DocumentGenerationCommandModel) TableName() string { return "document_generation_commands" }

type ArtifactDependencyModel struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID         uuid.UUID  `gorm:"type:uuid;not null;index"`
	SourceArtifactID  uuid.UUID  `gorm:"type:uuid;not null"`
	SourceRevisionID  uuid.UUID  `gorm:"type:uuid;not null"`
	SourceContentHash string     `gorm:"not null"`
	TargetArtifactID  uuid.UUID  `gorm:"type:uuid;not null"`
	TargetRevisionID  *uuid.UUID `gorm:"type:uuid"`
	Relation          string     `gorm:"not null"`
	Required          bool       `gorm:"not null"`
	CreatedBy         uuid.UUID  `gorm:"type:uuid;not null"`
	CreatedAt         time.Time
}

func (ArtifactDependencyModel) TableName() string { return "artifact_dependencies" }

type TraceLinkModel struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID        uuid.UUID `gorm:"type:uuid;not null;index"`
	SourceArtifactID uuid.UUID `gorm:"type:uuid;not null"`
	SourceRevisionID uuid.UUID `gorm:"type:uuid;not null"`
	SourceAnchorID   *string
	TargetArtifactID uuid.UUID  `gorm:"type:uuid;not null"`
	TargetRevisionID *uuid.UUID `gorm:"type:uuid"`
	TargetAnchorID   *string
	Relation         string          `gorm:"not null"`
	Metadata         json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedBy        uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt        time.Time
}

func (TraceLinkModel) TableName() string { return "trace_links" }

type ArtifactHealthModel struct {
	ArtifactID     uuid.UUID       `gorm:"type:uuid;primaryKey"`
	SyncStatus     string          `gorm:"not null"`
	DeliveryStatus string          `gorm:"not null"`
	FindingCount   int             `gorm:"not null"`
	BlockingCount  int             `gorm:"not null"`
	Report         json.RawMessage `gorm:"type:jsonb;not null"`
	ComputedAt     time.Time
}

func (ArtifactHealthModel) TableName() string { return "artifact_health" }

type ReviewRequestModel struct {
	ID          uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID   uuid.UUID       `gorm:"type:uuid;not null;index"`
	ArtifactID  uuid.UUID       `gorm:"type:uuid;not null"`
	RevisionID  uuid.UUID       `gorm:"type:uuid;not null"`
	ContentHash string          `gorm:"not null"`
	Status      string          `gorm:"not null"`
	Policy      json.RawMessage `gorm:"type:jsonb;not null"`
	RequestedBy uuid.UUID       `gorm:"type:uuid;not null"`
	RequestedAt time.Time
	ClosedAt    *time.Time
}

func (ReviewRequestModel) TableName() string { return "review_requests" }

type ReviewDecisionModel struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey"`
	ReviewRequestID uuid.UUID `gorm:"type:uuid;not null;index"`
	ReviewerID      uuid.UUID `gorm:"type:uuid;not null"`
	Decision        string    `gorm:"not null"`
	Summary         string    `gorm:"not null"`
	SoloSelfReview  bool      `gorm:"not null"`
	CreatedAt       time.Time
}

func (ReviewDecisionModel) TableName() string { return "review_decisions" }

type CommentThreadModel struct {
	ID         uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID  uuid.UUID       `gorm:"type:uuid;not null;index"`
	ArtifactID uuid.UUID       `gorm:"type:uuid;not null;index"`
	RevisionID *uuid.UUID      `gorm:"type:uuid"`
	Anchor     json.RawMessage `gorm:"type:jsonb;not null"`
	Severity   string          `gorm:"not null"`
	AssignedTo *uuid.UUID      `gorm:"type:uuid"`
	CreatedBy  uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt  time.Time
	ResolvedBy *uuid.UUID `gorm:"type:uuid"`
	ResolvedAt *time.Time
	OutdatedAt *time.Time
}

func (CommentThreadModel) TableName() string { return "comment_threads" }

type CommentMessageModel struct {
	ID              uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ThreadID        uuid.UUID       `gorm:"type:uuid;not null;index"`
	ParentMessageID *uuid.UUID      `gorm:"type:uuid"`
	Body            string          `gorm:"not null"`
	Mentions        json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedBy       uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt       time.Time
	EditedAt        *time.Time
	DeletedAt       *time.Time
}

func (CommentMessageModel) TableName() string { return "comment_messages" }

type InputManifestModel struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID     uuid.UUID `gorm:"type:uuid;not null;index"`
	Kind          string    `gorm:"not null"`
	SchemaVersion int       `gorm:"not null"`
	ContentStore  string    `gorm:"not null"`
	ContentRef    string    `gorm:"not null"`
	ContentHash   string    `gorm:"not null"`
	ManifestHash  string    `gorm:"not null"`
	CreatedBy     uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt     time.Time
}

func (InputManifestModel) TableName() string { return "input_manifests" }

type OutputProposalModel struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID       uuid.UUID  `gorm:"type:uuid;not null;index"`
	ArtifactID      *uuid.UUID `gorm:"type:uuid"`
	Kind            string     `gorm:"not null"`
	InputManifestID uuid.UUID  `gorm:"type:uuid;not null"`
	BaseRevisionID  *uuid.UUID `gorm:"type:uuid"`
	BaseDraftID     *uuid.UUID `gorm:"type:uuid"`
	BaseContentHash *string
	Status          string    `gorm:"not null"`
	Version         uint64    `gorm:"not null"`
	ContentStore    string    `gorm:"not null"`
	ContentRef      string    `gorm:"not null"`
	ContentHash     string    `gorm:"not null"`
	PayloadHash     string    `gorm:"not null"`
	OperationCount  int       `gorm:"not null"`
	AcceptedCount   int       `gorm:"not null"`
	RejectedCount   int       `gorm:"not null"`
	AIProvider      *string   `gorm:"column:ai_provider"`
	AIModel         *string   `gorm:"column:ai_model"`
	CreatedBy       uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt       time.Time
	AppliedBy       *uuid.UUID `gorm:"type:uuid"`
	AppliedAt       *time.Time
}

func (OutputProposalModel) TableName() string { return "output_proposals" }

type ProposalOperationDecisionModel struct {
	ProposalID  uuid.UUID `gorm:"type:uuid;primaryKey"`
	OperationID string    `gorm:"primaryKey"`
	Decision    string    `gorm:"not null"`
	Reason      string    `gorm:"not null"`
	DecidedBy   uuid.UUID `gorm:"type:uuid;not null"`
	DecidedAt   time.Time
}

func (ProposalOperationDecisionModel) TableName() string { return "proposal_operation_decisions" }

type DeliverySliceModel struct {
	ID                  uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID           uuid.UUID  `gorm:"type:uuid;not null;index"`
	SliceKey            string     `gorm:"not null"`
	Title               string     `gorm:"not null"`
	BlueprintRevisionID uuid.UUID  `gorm:"type:uuid;not null"`
	PageSpecRevisionID  *uuid.UUID `gorm:"type:uuid"`
	PrototypeRevisionID *uuid.UUID `gorm:"type:uuid"`
	SyncStatus          string     `gorm:"not null"`
	WorkflowStatus      string     `gorm:"not null"`
	OwnerID             *uuid.UUID `gorm:"type:uuid"`
	BlockerReason       string     `gorm:"not null"`
	UpdatedAt           time.Time
}

func (DeliverySliceModel) TableName() string { return "delivery_slices" }

type ImpactReportModel struct {
	ID               uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID        uuid.UUID       `gorm:"type:uuid;not null;index"`
	SourceArtifactID uuid.UUID       `gorm:"type:uuid;not null"`
	FromRevisionID   uuid.UUID       `gorm:"type:uuid;not null"`
	ToRevisionID     uuid.UUID       `gorm:"type:uuid;not null"`
	Status           string          `gorm:"not null"`
	Report           json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedBy        uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt        time.Time
	ResolvedAt       *time.Time
}

func (ImpactReportModel) TableName() string { return "impact_reports" }

type WorkflowDefinitionModel struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID   *uuid.UUID `gorm:"type:uuid;index"`
	WorkflowKey string     `gorm:"not null"`
	Title       string     `gorm:"not null"`
	Description string     `gorm:"not null"`
	Lifecycle   string     `gorm:"not null"`
	CreatedBy   uuid.UUID  `gorm:"type:uuid;not null"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (WorkflowDefinitionModel) TableName() string { return "workflow_definitions" }

type WorkflowDefinitionVersionModel struct {
	ID                      uuid.UUID       `gorm:"type:uuid;primaryKey"`
	DefinitionID            uuid.UUID       `gorm:"type:uuid;not null;index"`
	Version                 int             `gorm:"not null"`
	SchemaVersion           int             `gorm:"not null"`
	Content                 json.RawMessage `gorm:"type:jsonb;not null"`
	ContentHash             string          `gorm:"not null"`
	ExecutionProfileVersion string          `gorm:"not null"`
	ExecutionProfileHash    string          `gorm:"not null"`
	ValidationReport        json.RawMessage `gorm:"type:jsonb;not null"`
	Published               bool            `gorm:"not null"`
	CreatedBy               uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt               time.Time
}

func (WorkflowDefinitionVersionModel) TableName() string { return "workflow_definition_versions" }

type WorkflowRunModel struct {
	ID                      uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID               uuid.UUID       `gorm:"type:uuid;not null;index"`
	DefinitionVersionID     uuid.UUID       `gorm:"type:uuid;not null"`
	ExecutionProfileVersion string          `gorm:"not null"`
	ExecutionProfileHash    string          `gorm:"not null"`
	Status                  string          `gorm:"not null"`
	GovernanceMode          string          `gorm:"not null;default:team"`
	InputManifestID         *uuid.UUID      `gorm:"type:uuid"`
	Scope                   json.RawMessage `gorm:"type:jsonb;not null"`
	Context                 json.RawMessage `gorm:"type:jsonb;not null"`
	EventCursor             uint64          `gorm:"not null"`
	StartedBy               uuid.UUID       `gorm:"type:uuid;not null"`
	StartedAt               *time.Time
	CompletedAt             *time.Time
	CancelledAt             *time.Time
	Failure                 json.RawMessage `gorm:"type:jsonb"`
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (WorkflowRunModel) TableName() string { return "workflow_runs" }

type WorkflowNodeRunModel struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey"`
	RunID            uuid.UUID  `gorm:"type:uuid;not null;index"`
	NodeKey          string     `gorm:"not null"`
	NodeType         string     `gorm:"not null"`
	Status           string     `gorm:"not null"`
	Attempt          int        `gorm:"not null"`
	InputManifestID  *uuid.UUID `gorm:"type:uuid"`
	OutputProposalID *uuid.UUID `gorm:"type:uuid"`
	OutputRevisionID *uuid.UUID `gorm:"type:uuid"`
	LeaseOwner       *string
	LeaseExpiresAt   *time.Time
	AvailableAt      time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	Failure          json.RawMessage `gorm:"type:jsonb"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (WorkflowNodeRunModel) TableName() string { return "workflow_node_runs" }

type WorkflowRunEventModel struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	RunID     uuid.UUID `gorm:"type:uuid;not null;index"`
	Sequence  uint64    `gorm:"not null"`
	EventType string    `gorm:"not null"`
	NodeKey   *string
	Payload   json.RawMessage `gorm:"type:jsonb;not null"`
	ActorID   *uuid.UUID      `gorm:"type:uuid"`
	CreatedAt time.Time
}

func (WorkflowRunEventModel) TableName() string { return "workflow_run_events" }

type ConversationModel struct {
	ID                      uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID               uuid.UUID `gorm:"type:uuid;not null;index"`
	Title                   string    `gorm:"not null"`
	Status                  string    `gorm:"not null"`
	Version                 uint64    `gorm:"not null"`
	CreatedBy               uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt               time.Time
	UpdatedAt               time.Time
	ArchivedAt              *time.Time
	SummaryCheckpointHeadID *uuid.UUID `gorm:"type:uuid"`
}

func (ConversationModel) TableName() string { return "conversations" }

type ConversationMessageModel struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ConversationID uuid.UUID  `gorm:"type:uuid;not null;index"`
	Sequence       uint64     `gorm:"not null"`
	Role           string     `gorm:"not null"`
	Content        string     `gorm:"not null"`
	ProposalID     *uuid.UUID `gorm:"type:uuid"`
	CreatedBy      uuid.UUID  `gorm:"type:uuid;not null"`
	CreatedAt      time.Time
}

func (ConversationMessageModel) TableName() string { return "conversation_messages" }

type ConversationSummaryCheckpointModel struct {
	ID                   uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID            uuid.UUID  `gorm:"type:uuid;not null;index"`
	ConversationID       uuid.UUID  `gorm:"type:uuid;not null;index"`
	PreviousCheckpointID *uuid.UUID `gorm:"type:uuid"`
	ThroughMessageID     uuid.UUID  `gorm:"type:uuid;not null"`
	ThroughSequence      uint64     `gorm:"not null"`
	MessageCount         uint64     `gorm:"not null"`
	ContentBytes         uint64     `gorm:"not null"`
	PrefixHash           []byte     `gorm:"type:bytea;not null"`
	HashAlgorithm        string     `gorm:"not null"`
	Summary              string     `gorm:"not null"`
	SummaryHash          []byte     `gorm:"type:bytea;not null"`
	Status               string     `gorm:"not null"`
	Version              uint64     `gorm:"not null"`
	CreatedBy            uuid.UUID  `gorm:"type:uuid;not null"`
	CreatedAt            time.Time
	ReviewedBy           *uuid.UUID `gorm:"type:uuid"`
	ReviewedAt           *time.Time
	ReviewReason         string `gorm:"not null"`
}

func (ConversationSummaryCheckpointModel) TableName() string {
	return "conversation_summary_checkpoints"
}

type WorkflowIntentProposalModel struct {
	ID                           uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID                    uuid.UUID       `gorm:"type:uuid;not null;index"`
	ConversationID               uuid.UUID       `gorm:"type:uuid;not null;index"`
	TriggerMessageID             uuid.UUID       `gorm:"type:uuid;not null"`
	AssistantMessageID           uuid.UUID       `gorm:"type:uuid;not null"`
	Kind                         string          `gorm:"not null"`
	Status                       string          `gorm:"not null"`
	Version                      uint64          `gorm:"not null"`
	SuggestedDefinitionVersionID uuid.UUID       `gorm:"type:uuid;not null"`
	DesiredOutputCapability      string          `gorm:"not null;default:application"`
	Scope                        json.RawMessage `gorm:"type:jsonb;not null"`
	SourceRefs                   json.RawMessage `gorm:"type:jsonb;not null"`
	ManifestIntent               json.RawMessage `gorm:"type:jsonb;not null"`
	WorkbenchInstruction         json.RawMessage `gorm:"type:jsonb;not null"`
	Origin                       string          `gorm:"not null"`
	AIProvider                   *string         `gorm:"column:ai_provider"`
	AIModel                      *string         `gorm:"column:ai_model"`
	AIResponseID                 *string         `gorm:"column:ai_response_id"`
	SummaryCheckpointID          *uuid.UUID      `gorm:"type:uuid"`
	ConversationContext          json.RawMessage `gorm:"type:jsonb;not null"`
	ProviderInputHash            []byte          `gorm:"type:bytea"`
	DecisionReason               string          `gorm:"not null"`
	ProposedBy                   uuid.UUID       `gorm:"type:uuid;not null"`
	DecidedBy                    *uuid.UUID      `gorm:"type:uuid"`
	CreatedAt                    time.Time
	DecidedAt                    *time.Time
}

func (WorkflowIntentProposalModel) TableName() string { return "workflow_intent_proposals" }

type ConversationCommandModel struct {
	ID                  uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID           uuid.UUID       `gorm:"type:uuid;not null;index"`
	ConversationID      uuid.UUID       `gorm:"type:uuid;not null;index"`
	ProposalID          uuid.UUID       `gorm:"type:uuid;not null;uniqueIndex"`
	Kind                string          `gorm:"not null"`
	Status              string          `gorm:"not null"`
	Version             uint64          `gorm:"not null"`
	Payload             json.RawMessage `gorm:"type:jsonb;not null"`
	Result              json.RawMessage `gorm:"type:jsonb"`
	Failure             json.RawMessage `gorm:"type:jsonb"`
	SummaryCheckpointID *uuid.UUID      `gorm:"type:uuid"`
	ConversationContext json.RawMessage `gorm:"type:jsonb;not null"`
	ProviderInputHash   []byte          `gorm:"type:bytea"`
	AcceptedBy          uuid.UUID       `gorm:"type:uuid;not null"`
	ExecutionActorID    *uuid.UUID      `gorm:"type:uuid"`
	ExecutionClaim      *uuid.UUID      `gorm:"type:uuid"`
	ClaimExpiresAt      *time.Time
	ExecutedBy          *uuid.UUID `gorm:"type:uuid"`
	RejectedBy          *uuid.UUID `gorm:"type:uuid"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ExecutedAt          *time.Time
	RejectedAt          *time.Time
	FailedAt            *time.Time
}

func (ConversationCommandModel) TableName() string { return "conversation_commands" }

type ApplicationBuildManifestModel struct {
	ID                  uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID           uuid.UUID  `gorm:"type:uuid;not null;index"`
	WorkflowRunID       *uuid.UUID `gorm:"type:uuid"`
	RootManifestID      uuid.UUID  `gorm:"type:uuid;not null"`
	DerivedFromID       *uuid.UUID `gorm:"type:uuid"`
	WorkspaceRevisionID *uuid.UUID `gorm:"type:uuid"`
	RootOrdinal         *int
	ManifestGroupKey    *string
	DeliverySliceID     *string
	SchemaVersion       int       `gorm:"not null"`
	ContentStore        string    `gorm:"not null"`
	ContentRef          string    `gorm:"not null"`
	ContentHash         string    `gorm:"not null"`
	ManifestHash        string    `gorm:"not null"`
	Status              string    `gorm:"not null"`
	CreatedBy           uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt           time.Time
	InvalidatedAt       *time.Time
	InvalidationReason  *string
}

func (ApplicationBuildManifestModel) TableName() string { return "application_build_manifests" }

type ImplementationProposalModel struct {
	ID                                  uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID                           uuid.UUID  `gorm:"type:uuid;not null;index"`
	BuildManifestID                     uuid.UUID  `gorm:"type:uuid;not null"`
	ApplicationBuildContractID          *uuid.UUID `gorm:"type:uuid"`
	ApplicationBuildContractHash        *string
	BaseWorkspaceRevisionID             *uuid.UUID `gorm:"type:uuid"`
	ExecutionSource                     string     `gorm:"not null;default:manual_submission"`
	ConversationCommandID               *uuid.UUID `gorm:"type:uuid"`
	SupersedesProposalID                *uuid.UUID `gorm:"type:uuid"`
	InstructionHash                     *string
	AIProvider                          *string    `gorm:"column:ai_provider"`
	AIModel                             *string    `gorm:"column:ai_model"`
	CandidateSnapshotID                 *uuid.UUID `gorm:"type:uuid"`
	CandidateBaseTreeHash               *string
	CandidateTreeHash                   *string
	CandidateVerificationBindingVersion *string
	CandidateVerificationReceiptID      *uuid.UUID `gorm:"type:uuid"`
	CandidateVerificationReceiptHash    *string
	Status                              string `gorm:"not null"`
	Version                             uint64 `gorm:"not null"`
	ContentStore                        string `gorm:"not null"`
	ContentRef                          string `gorm:"not null"`
	ContentHash                         string `gorm:"not null"`
	PayloadHash                         string `gorm:"not null"`
	OperationCount                      int    `gorm:"not null"`
	// Nullable preserves the distinction between historical proposals whose
	// content was never projected and new proposals whose completeness was
	// counted from the exact immutable payload.
	UnimplementedCount      *int
	BlockingDiagnosticCount *int
	AcceptedCount           int       `gorm:"not null"`
	RejectedCount           int       `gorm:"not null"`
	CreatedBy               uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt               time.Time
	AppliedBy               *uuid.UUID `gorm:"type:uuid"`
	AppliedAt               *time.Time
}

func (ImplementationProposalModel) TableName() string { return "implementation_proposals" }

type CandidateImplementationFreezeModel struct {
	ID                         uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID                  uuid.UUID  `gorm:"type:uuid;not null;index"`
	SessionID                  uuid.UUID  `gorm:"type:uuid;not null"`
	CandidateID                uuid.UUID  `gorm:"type:uuid;not null;index"`
	CandidateSnapshotID        uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex"`
	ImplementationProposalID   uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex"`
	RequestKey                 string     `gorm:"not null"`
	RequestHash                string     `gorm:"not null"`
	SessionVersion             uint64     `gorm:"not null"`
	CandidateVersion           uint64     `gorm:"not null"`
	JournalSequence            uint64     `gorm:"not null"`
	SessionEpoch               uint64     `gorm:"not null"`
	WriterLeaseEpoch           uint64     `gorm:"not null"`
	BaseTreeHash               string     `gorm:"not null"`
	CandidateTreeStore         string     `gorm:"not null"`
	CandidateTreeOwnerID       uuid.UUID  `gorm:"type:uuid;not null"`
	CandidateTreeRef           string     `gorm:"not null"`
	CandidateTreeContentHash   string     `gorm:"not null"`
	CandidateTreeHash          string     `gorm:"not null"`
	VerificationBindingVersion string     `gorm:"not null"`
	VerificationReceiptID      *uuid.UUID `gorm:"type:uuid"`
	VerificationReceiptHash    *string
	BuildManifestID            uuid.UUID  `gorm:"type:uuid;not null"`
	BuildManifestHash          string     `gorm:"not null"`
	BuildContractID            uuid.UUID  `gorm:"type:uuid;not null"`
	BuildContractHash          string     `gorm:"not null"`
	FullStackTemplateID        uuid.UUID  `gorm:"type:uuid;not null"`
	FullStackTemplateHash      string     `gorm:"not null"`
	BaseWorkspaceArtifactID    *uuid.UUID `gorm:"type:uuid"`
	BaseWorkspaceRevisionID    *uuid.UUID `gorm:"type:uuid"`
	BaseWorkspaceContentHash   *string
	ProposalPayloadHash        string    `gorm:"not null"`
	OperationCount             int       `gorm:"not null"`
	Reason                     string    `gorm:"not null"`
	CreatedBy                  uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt                  time.Time
}

func (CandidateImplementationFreezeModel) TableName() string {
	return "candidate_implementation_freezes"
}

type ImplementationGenerationClaimModel struct {
	ID                            uuid.UUID  `gorm:"type:uuid;primaryKey"`
	BuildManifestID               uuid.UUID  `gorm:"type:uuid;not null;index"`
	ProjectID                     uuid.UUID  `gorm:"type:uuid;not null;index"`
	ApplicationBuildContractID    *uuid.UUID `gorm:"type:uuid"`
	ApplicationBuildContractHash  *string
	RootManifestID                uuid.UUID  `gorm:"type:uuid;not null"`
	RequestKey                    uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex"`
	ReservedProposalID            uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex"`
	ExecutionSource               string     `gorm:"not null"`
	ConversationCommandID         *uuid.UUID `gorm:"type:uuid;uniqueIndex"`
	GovernanceManifestID          *uuid.UUID `gorm:"type:uuid"`
	GovernanceManifestHash        *string
	GovernanceSourceRefs          json.RawMessage `gorm:"type:jsonb"`
	Instruction                   json.RawMessage `gorm:"type:jsonb;not null"`
	InstructionHash               string          `gorm:"not null"`
	RequestedModel                string          `gorm:"not null"`
	GenerationContractVersion     string          `gorm:"not null"`
	SystemPromptHash              string          `gorm:"not null"`
	OutputSchemaHash              string          `gorm:"not null"`
	ActorID                       uuid.UUID       `gorm:"type:uuid;not null"`
	ExpectedActiveProposalID      *uuid.UUID      `gorm:"type:uuid"`
	ExpectedActiveProposalVersion *uint64
	ClaimToken                    *uuid.UUID `gorm:"type:uuid"`
	ClaimExpiresAt                *time.Time
	Status                        string     `gorm:"not null"`
	AttemptCount                  int        `gorm:"not null"`
	CompletedProposalID           *uuid.UUID `gorm:"type:uuid"`
	LastFailure                   *string
	LastFailedAt                  *time.Time
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

func (ImplementationGenerationClaimModel) TableName() string {
	return "implementation_generation_claims"
}

type ImplementationOperationDecisionModel struct {
	ProposalID  uuid.UUID `gorm:"type:uuid;primaryKey"`
	OperationID string    `gorm:"primaryKey"`
	Decision    string    `gorm:"not null"`
	Reason      string    `gorm:"not null"`
	DecidedBy   uuid.UUID `gorm:"type:uuid;not null"`
	DecidedAt   time.Time
}

func (ImplementationOperationDecisionModel) TableName() string {
	return "implementation_operation_decisions"
}

type NotificationModel struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID       uuid.UUID `gorm:"type:uuid;not null;index"`
	ProjectID    uuid.UUID `gorm:"type:uuid;not null"`
	Kind         string    `gorm:"not null"`
	Title        string    `gorm:"not null"`
	Body         string    `gorm:"not null"`
	ResourceType string    `gorm:"not null"`
	ResourceID   string    `gorm:"not null"`
	CreatedAt    time.Time
	ReadAt       *time.Time
}

func (NotificationModel) TableName() string { return "notifications" }

type IdempotencyRecordModel struct {
	Scope           string `gorm:"primaryKey"`
	IdempotencyKey  string `gorm:"primaryKey"`
	RequestHash     string `gorm:"not null"`
	ResponseStatus  *int
	ResponseHeaders json.RawMessage `gorm:"type:jsonb"`
	ResponseBody    []byte
	ResourceType    *string
	ResourceID      *string
	LockedUntil     *time.Time
	ExpiresAt       time.Time `gorm:"not null;index"`
	CreatedAt       time.Time
	CompletedAt     *time.Time
}

func (IdempotencyRecordModel) TableName() string { return "idempotency_records" }

type AuditEventModel struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID  *uuid.UUID `gorm:"type:uuid;index"`
	ActorID    *uuid.UUID `gorm:"type:uuid"`
	RequestID  *string
	Action     string          `gorm:"not null"`
	TargetType string          `gorm:"not null"`
	TargetID   string          `gorm:"not null"`
	Metadata   json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedAt  time.Time
}

func (AuditEventModel) TableName() string { return "audit_events" }

type OutboxEventModel struct {
	ID            uuid.UUID       `gorm:"type:uuid;primaryKey"`
	AggregateType string          `gorm:"not null"`
	AggregateID   string          `gorm:"not null"`
	EventType     string          `gorm:"not null"`
	Subject       string          `gorm:"not null"`
	Payload       json.RawMessage `gorm:"type:jsonb;not null"`
	Headers       json.RawMessage `gorm:"type:jsonb;not null"`
	Attempts      int             `gorm:"not null"`
	AvailableAt   time.Time
	PublishedAt   *time.Time
	LastError     *string
	CreatedAt     time.Time
}

func (OutboxEventModel) TableName() string { return "outbox_events" }
