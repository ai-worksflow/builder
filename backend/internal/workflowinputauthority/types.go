// Package workflowinputauthority defines the immutable, version-1 Workflow
// input snapshot issued when the dedicated external-qualification gate is
// activated.
//
// This package is a pure canonical/domain boundary. It does not read mutable
// Workflow state, begin transactions, activate nodes, issue reviews, or
// authorize qualification or promotion.
package workflowinputauthority

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	FreezeRequestSchemaV1 = "worksflow-workflow-input-freeze-request/v1"
	InputSchemaV1         = "worksflow-workflow-input/v1"
	AuthoritySchemaV1     = "worksflow-workflow-input-authority/v1"

	FreezeRequestMediaTypeV1 = "application/vnd.worksflow.workflow-input-freeze-request+json;version=1"
	InputMediaTypeV1         = "application/vnd.worksflow.workflow-input+json;version=1"
	AuthorityMediaTypeV1     = "application/vnd.worksflow.workflow-input-authority+json;version=1"

	FreezeRequestHashDomainV1 = "worksflow.workflow-input.freeze-request/v1"
	TargetHashDomainV1        = "worksflow.workflow-input.target/v1"
	InputHashDomainV1         = "worksflow.workflow-input.input/v1"
	AuthorityHashDomainV1     = "worksflow.workflow-input.authority/v1"

	ExecutionProfileV3           = "workflow-engine/v3"
	CanonicalReviewReceiptV1     = "worksflow-canonical-review-approval-receipt/v1"
	QualificationPlanAuthorityV1 = "worksflow-qualification-plan-authority/v1"
	QualificationReceiptV3       = "worksflow-qualification-receipt/v3"
	QualificationPromotionV2     = "worksflow-qualification-promotion-consume/v2"

	ExternalQualificationGate      = "external-qualification"
	ExternalQualificationNodeType  = "external_qualification_gate"
	ExternalQualificationPolicyV1  = "external-qualification/v1"
	CompletedSourceStatus          = "completed"
	ConsumedBuildManifestStatus    = "consumed"
	ReadyBuildContractStatus       = "ready"
	ApprovedRevisionStatus         = "approved"
	GovernanceSolo                 = "solo"
	GovernanceTeam                 = "team"
	SliceKindRoot                  = "root"
	SliceKindDelivery              = "slice"
	MappingKindIdentity            = "identity"
	MappingKindObjectMap           = "object-map"
	CurrencyExactApproved          = "exact-approved"
	CurrencyLatestApprovedRequired = "latest-approved-required"
	RevisionPurposeWorkspaceTarget = "workspace-target"
	ManifestRoleRun                = "run"
	ManifestRolePredecessor        = "predecessor"
	ManifestRoleNode               = "node"
	ManifestRoleQualification      = "qualification"

	MaximumPredecessors       = 1024
	MaximumManifests          = 1024
	MaximumRevisions          = 2048
	MaximumReviewReceipts     = 2048
	MaximumRequestBytes       = 64 << 10
	MaximumTargetBytes        = 256 << 10
	MaximumAuthorityBytes     = 1 << 20
	MaximumInputBytes         = 32 << 20
	MaximumCandidateBytes     = 64 << 20
	MaximumDefinitionBytes    = 8 << 20
	MaximumRunScopeBytes      = 8 << 20
	MaximumNodeInputBytes     = 16 << 20
	MaximumBuildManifestBytes = 8 << 20
	MaximumBuildContractBytes = 8 << 20
	MaximumManifestBytes      = 8 << 20
	MaximumRevisionBytes      = 16 << 20
	MaximumReviewReceiptBytes = 1 << 20
	MaximumRetainedBytes      = 128 << 20
	MaximumCanonicalBytes     = MaximumInputBytes
	MaximumJavaScriptSafeInt  = int64(9007199254740991)
)

var (
	ErrInvalid        = errors.New("workflow input authority is invalid")
	ErrNotFound       = errors.New("workflow input authority is not found")
	ErrConflict       = errors.New("workflow input authority conflicts with immutable state")
	ErrStale          = errors.New("workflow input authority is stale")
	ErrOutcomeUnknown = errors.New("workflow input authority outcome is unknown; inspect the same operation or node")
)

// FreezeRequest is derived by the Workflow service. Its IDs and expected
// cursor are not public command fields.
type FreezeRequest struct {
	AuthorityID       string `json:"authorityId"`
	ExpectedRunCursor int64  `json:"expectedRunCursor"`
	MediaType         string `json:"mediaType"`
	NodeKey           string `json:"nodeKey"`
	NodeRunID         string `json:"nodeRunId"`
	OperationID       string `json:"operationId"`
	ProjectID         string `json:"projectId"`
	SchemaVersion     string `json:"schemaVersion"`
	WorkflowRunID     string `json:"workflowRunId"`
}

// The private candidate document is the sole JSON argument accepted by the
// PostgreSQL issuer. It is deliberately distinct from the public authority
// wire and has exactly these six members.
type FreezeCandidateDocument struct {
	InputManifests      []ManifestCandidate          `json:"inputManifests"`
	ManifestSubject     string                       `json:"manifestSubject"`
	QualificationPolicy QualificationPolicyBinding   `json:"qualificationPolicy"`
	QualityResult       CandidateQualityResult       `json:"qualityResult"`
	ReviewRequirements  []ReviewRequirementCandidate `json:"reviewRequirements"`
	Revisions           []RevisionCandidate          `json:"revisions"`
}

type ManifestCandidate struct {
	ManifestID  string `json:"manifestId"`
	RawBytesHex string `json:"rawBytesHex"`
	Role        string `json:"role"`
}

type RevisionCandidate struct {
	CanonicalReviewRequired bool   `json:"canonicalReviewRequired"`
	CurrencyPolicy          string `json:"currencyPolicy"`
	Purpose                 string `json:"purpose"`
	RawBytesHex             string `json:"rawBytesHex"`
	RevisionID              string `json:"revisionId"`
}

type ReviewRequirementCandidate struct {
	Purpose    string `json:"purpose"`
	RevisionID string `json:"revisionId"`
}

type CandidateQualityResult struct {
	BuildContractHash            string `json:"buildContractHash"`
	BuildContractID              string `json:"buildContractId"`
	BuildManifestHash            string `json:"buildManifestHash"`
	BuildManifestID              string `json:"buildManifestId"`
	Passed                       bool   `json:"passed"`
	QualityRunID                 string `json:"qualityRunId"`
	WorkspaceRevisionContentHash string `json:"workspaceRevisionContentHash"`
	WorkspaceRevisionID          string `json:"workspaceRevisionId"`
}

type ProjectBinding struct {
	GovernanceMode string `json:"governanceMode"`
	ID             string `json:"id"`
}

type DefinitionBinding struct {
	DefinitionHash          string `json:"definitionHash"`
	DefinitionID            string `json:"definitionId"`
	DefinitionVersion       int64  `json:"definitionVersion"`
	DefinitionVersionID     string `json:"definitionVersionId"`
	ExecutionProfileHash    string `json:"executionProfileHash"`
	ExecutionProfileVersion string `json:"executionProfileVersion"`
	RawBytesHash            string `json:"rawBytesHash"`
	RawBytesSize            int64  `json:"rawBytesSize"`
}

type RunBinding struct {
	ID                string `json:"id"`
	InputManifestHash string `json:"inputManifestHash"`
	InputManifestID   string `json:"inputManifestId"`
	ScopeRawBytesHash string `json:"scopeRawBytesHash"`
	ScopeRawBytesSize int64  `json:"scopeRawBytesSize"`
	StartedAt         string `json:"startedAt"`
	StartedBy         string `json:"startedBy"`
}

// SliceIdentity is the closed root-or-slice discriminator. ID is absent only
// for root, and is a UUIDv4 for slice.
type SliceIdentity struct {
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind"`
}

type GateBinding struct {
	ActivationEventID       string        `json:"activationEventId"`
	ActivationEventSequence int64         `json:"activationEventSequence"`
	DefinitionNodeID        string        `json:"definitionNodeId"`
	GateName                string        `json:"gateName"`
	NodeKey                 string        `json:"nodeKey"`
	NodeRunID               string        `json:"nodeRunId"`
	NodeType                string        `json:"nodeType"`
	SliceIdentity           SliceIdentity `json:"sliceIdentity"`
	StageGate               string        `json:"stageGate"`
}

type NodeInputBinding struct {
	BindingCount int64  `json:"bindingCount"`
	RawBytesHash string `json:"rawBytesHash"`
	RawBytesSize int64  `json:"rawBytesSize"`
	SemanticHash string `json:"semanticHash"`
}

type ManifestReference struct {
	Hash string `json:"hash"`
	ID   string `json:"id"`
}

type ProposalReference struct {
	ID          string `json:"id"`
	PayloadHash string `json:"payloadHash"`
}

type ArtifactRevisionReference struct {
	AnchorID    string `json:"anchorId,omitempty"`
	ArtifactID  string `json:"artifactId"`
	ContentHash string `json:"contentHash"`
	RevisionID  string `json:"revisionId"`
}

type ProposalLineagePin struct {
	Manifest                 ManifestReference `json:"manifest"`
	ProducerDefinitionNodeID string            `json:"producerDefinitionNodeId"`
	ProducerNodeKey          string            `json:"producerNodeKey"`
	Proposal                 ProposalReference `json:"proposal"`
}

type DeliverySliceReference struct {
	Blueprint    *ArtifactRevisionReference `json:"blueprint,omitempty"`
	FanOutNodeID string                     `json:"fanOutNodeId"`
	ID           string                     `json:"id"`
	Key          string                     `json:"key"`
	PageSpec     *ArtifactRevisionReference `json:"pageSpec,omitempty"`
	Prototype    *ArtifactRevisionReference `json:"prototype,omitempty"`
}

// PredecessorBinding is the exact migration-78 output projection rebuilt from
// one retained v3 NodeInput binding and locked source-node facts.
type PredecessorBinding struct {
	ArtifactRevisions             []ArtifactRevisionReference `json:"artifactRevisions"`
	BindingRawBytesHash           string                      `json:"bindingRawBytesHash"`
	DeliverySliceRefs             []DeliverySliceReference    `json:"deliverySliceRefs"`
	EdgeID                        string                      `json:"edgeId"`
	InputManifest                 *ManifestReference          `json:"inputManifest"`
	MappingHash                   string                      `json:"mappingHash"`
	MappingKind                   string                      `json:"mappingKind"`
	MappingOrdinal                int64                       `json:"mappingOrdinal"`
	MaterializedArtifactRevisions []ArtifactRevisionReference `json:"materializedArtifactRevisions"`
	OutputHash                    string                      `json:"outputHash"`
	OutputProposal                *ProposalReference          `json:"outputProposal"`
	OutputRevisionNumber          int64                       `json:"outputRevisionNumber"`
	ProposalPins                  []ProposalLineagePin        `json:"proposalPins"`
	SourceDefinitionNodeID        string                      `json:"sourceDefinitionNodeId"`
	SourceNodeKey                 string                      `json:"sourceNodeKey"`
	SourceNodeRunID               string                      `json:"sourceNodeRunId"`
	SourceNodeType                string                      `json:"sourceNodeType"`
	SourcePort                    string                      `json:"sourcePort"`
	SourceSliceIdentity           SliceIdentity               `json:"sourceSliceIdentity"`
	SourceStatus                  string                      `json:"sourceStatus"`
	TargetPort                    string                      `json:"targetPort"`
	ValueHash                     string                      `json:"valueHash"`
}

type InputManifestBinding struct {
	ContentHash   string `json:"contentHash"`
	ContentRef    string `json:"contentRef"`
	ContentStore  string `json:"contentStore"`
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	ManifestHash  string `json:"manifestHash"`
	ProjectID     string `json:"projectId"`
	RawBytesHash  string `json:"rawBytesHash"`
	RawBytesSize  int64  `json:"rawBytesSize"`
	Role          string `json:"role"`
	SchemaVersion int64  `json:"schemaVersion"`
}

type RevisionBinding struct {
	ArtifactID               string  `json:"artifactId"`
	ArtifactKind             string  `json:"artifactKind"`
	ByteSize                 int64   `json:"byteSize"`
	CanonicalReviewRequired  bool    `json:"canonicalReviewRequired"`
	ChangeSourceAtFreeze     string  `json:"changeSourceAtFreeze"`
	ContentHash              string  `json:"contentHash"`
	ContentRef               string  `json:"contentRef"`
	ContentStore             string  `json:"contentStore"`
	CurrencyPolicy           string  `json:"currencyPolicy"`
	ImplementationProposalID *string `json:"implementationProposalId"`
	IsLatestApprovedAtFreeze bool    `json:"isLatestApprovedAtFreeze"`
	IsLatestCurrentAtFreeze  bool    `json:"isLatestCurrentAtFreeze"`
	ProposalID               *string `json:"proposalId"`
	Purpose                  string  `json:"purpose"`
	RawBytesHash             string  `json:"rawBytesHash"`
	RevisionID               string  `json:"revisionId"`
	SchemaVersion            int64   `json:"schemaVersion"`
	SourceRequiredAtFreeze   bool    `json:"sourceRequiredAtFreeze"`
	SourceManifestID         *string `json:"sourceManifestId"`
	WorkflowStatusAtFreeze   string  `json:"workflowStatusAtFreeze"`
}

type ReviewReceiptBinding struct {
	ArtifactID           string `json:"artifactId"`
	ProjectID            string `json:"projectId"`
	Purpose              string `json:"purpose"`
	ReceiptHash          string `json:"receiptHash"`
	ReceiptRawBytesHash  string `json:"receiptRawBytesHash"`
	ReceiptRawBytesSize  int64  `json:"receiptRawBytesSize"`
	ReceiptSchemaVersion string `json:"receiptSchemaVersion"`
	ReviewRequestID      string `json:"reviewRequestId"`
	RevisionContentHash  string `json:"revisionContentHash"`
	RevisionID           string `json:"revisionId"`
}

type BuildManifestBinding struct {
	ContentHash    string `json:"contentHash"`
	ID             string `json:"id"`
	ManifestHash   string `json:"manifestHash"`
	RawBytesHash   string `json:"rawBytesHash"`
	RawBytesSize   int64  `json:"rawBytesSize"`
	StatusAtFreeze string `json:"statusAtFreeze"`
}

type BuildContractBinding struct {
	ContentHash    string `json:"contentHash"`
	ContractHash   string `json:"contractHash"`
	ID             string `json:"id"`
	RawBytesHash   string `json:"rawBytesHash"`
	RawBytesSize   int64  `json:"rawBytesSize"`
	StatusAtFreeze string `json:"statusAtFreeze"`
}

type BuildBinding struct {
	BuildContract BuildContractBinding `json:"buildContract"`
	BuildManifest BuildManifestBinding `json:"buildManifest"`
}

type QualificationPolicyBinding struct {
	AuthorityHash      string `json:"authorityHash"`
	AuthorityID        string `json:"authorityId"`
	ExternalGatePolicy string `json:"externalGatePolicy"`
}

type QualityResultBinding struct {
	BuildManifestHash            string `json:"buildManifestHash"`
	BuildManifestID              string `json:"buildManifestId"`
	Passed                       bool   `json:"passed"`
	QualityRunID                 string `json:"qualityRunId"`
	WorkspaceRevisionContentHash string `json:"workspaceRevisionContentHash"`
	WorkspaceRevisionID          string `json:"workspaceRevisionId"`
}

type TargetDocument struct {
	ManifestSubject           string `json:"manifestSubject"`
	NodeKey                   string `json:"nodeKey"`
	ProjectID                 string `json:"projectId"`
	StageGate                 string `json:"stageGate"`
	TargetRevisionContentHash string `json:"targetRevisionContentHash"`
	TargetRevisionID          string `json:"targetRevisionId"`
	WorkflowRunID             string `json:"workflowRunId"`
}

type WorkflowInputDocument struct {
	Build               BuildBinding               `json:"build"`
	Definition          DefinitionBinding          `json:"definition"`
	Gate                GateBinding                `json:"gate"`
	InputManifests      []InputManifestBinding     `json:"inputManifests"`
	MediaType           string                     `json:"mediaType"`
	NodeInput           NodeInputBinding           `json:"nodeInput"`
	Predecessors        []PredecessorBinding       `json:"predecessors"`
	Project             ProjectBinding             `json:"project"`
	QualificationPolicy QualificationPolicyBinding `json:"qualificationPolicy"`
	QualityResult       QualityResultBinding       `json:"qualityResult"`
	ReviewReceipts      []ReviewReceiptBinding     `json:"reviewReceipts"`
	Revisions           []RevisionBinding          `json:"revisions"`
	Run                 RunBinding                 `json:"run"`
	SchemaVersion       string                     `json:"schemaVersion"`
	Target              TargetDocument             `json:"target"`
	TargetHash          string                     `json:"targetHash"`
}

// AuthorityEnvelope deliberately excludes AuthorityHash to keep the hash
// graph acyclic.
type AuthorityEnvelope struct {
	AuthorityID   string `json:"authorityId"`
	InputHash     string `json:"inputHash"`
	MediaType     string `json:"mediaType"`
	NodeRunID     string `json:"nodeRunId"`
	OperationID   string `json:"operationId"`
	ProjectID     string `json:"projectId"`
	RequestHash   string `json:"requestHash"`
	SchemaVersion string `json:"schemaVersion"`
	TargetHash    string `json:"targetHash"`
	WorkflowRunID string `json:"workflowRunId"`
}

type InputManifestMaterial struct {
	Bytes      []byte
	ManifestID string
	Role       string
}

type RevisionMaterial struct {
	Bytes      []byte
	Purpose    string
	RevisionID string
}

type ReviewReceiptMaterial struct {
	Bytes           []byte
	ReviewRequestID string
}

// RetainedMaterials contains exact legacy/external bytes. It is not a wire
// document and is never embedded into the strict public input document.
type RetainedMaterials struct {
	BuildContract  []byte
	BuildManifest  []byte
	Definition     []byte
	InputManifests []InputManifestMaterial
	NodeInput      []byte
	ReviewReceipts []ReviewReceiptMaterial
	Revisions      []RevisionMaterial
	RunScope       []byte
}

// Candidate is a server-resolved aggregate, not a persisted wire. Document is
// the exact six-field private issuer argument; Input is the expected output
// rebuilt from locked facts; Materials contains every retained byte sequence.
// Compile requires every repeated fact to agree before deriving hashes.
type Candidate struct {
	Document  FreezeCandidateDocument
	Input     WorkflowInputDocument
	Materials RetainedMaterials
	Request   FreezeRequest
}

// Record retains every independently verifiable byte authority. FrozenAt is
// assigned by Store and is excluded from every canonical hash.
type Record struct {
	OperationID   uuid.UUID
	AuthorityID   uuid.UUID
	WorkflowRunID uuid.UUID
	NodeRunID     uuid.UUID

	Request      FreezeRequest
	RequestBytes []byte
	RequestHash  string

	Target      TargetDocument
	TargetBytes []byte
	TargetHash  string

	Input      WorkflowInputDocument
	InputBytes []byte
	InputHash  string

	Envelope      AuthorityEnvelope
	EnvelopeBytes []byte
	AuthorityHash string

	Materials  RetainedMaterials
	FrozenAt   time.Time
	Idempotent bool
}

// Transaction is an opaque existing authority transaction. A production
// implementation wraps its already-open Workflow transaction; Store must not
// silently begin a second transaction.
type Transaction interface {
	workflowInputAuthorityTransaction()
}

// MemoryTransaction is the explicit token admitted by MemoryStore.
type MemoryTransaction struct{}

func (MemoryTransaction) workflowInputAuthorityTransaction() {}

// Store accepts a Candidate, never caller-authored authority bytes or hashes.
// A PostgreSQL implementation passes only private candidate/raw facts to the
// database issuer, independently decodes the returned bytes before commit,
// and participates in the caller-owned transaction.
type Store interface {
	Freeze(context.Context, Transaction, Candidate) (Record, error)
	InspectOperation(context.Context, uuid.UUID) (Record, error)
	ResolveAuthority(context.Context, uuid.UUID) (Record, error)
	ResolveNode(context.Context, uuid.UUID, uuid.UUID) (Record, error)
	AssertCurrent(context.Context, uuid.UUID) (Record, error)
}
