// Package qualificationhandoff defines the closed, non-secret production
// boundary that materializes one immutable Qualification Promotion v2 handoff.
//
// The package deliberately exposes only a single opaque handoff identity to
// the database capability. Project, workflow, Revision, authority, and event
// facts are returned by PostgreSQL and are never accepted from the caller.
package qualificationhandoff

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

const (
	BundleSchemaV1            = "worksflow-qualification-promotion-handoff-completion-bundle/v1"
	CompletionSchemaV1        = "worksflow-qualification-promotion-handoff-completion/v1"
	RevisionAuthoritySchemaV1 = "worksflow-qualification-promotion-output-revision-authorities/v1"
	CopiedLineageSchemaV1     = "worksflow-qualification-handoff-copied-lineage/v1"

	HandoffHashPrefixV1       = "worksflow-qualification-handoff-hash/v1"
	CompletionHashDomainV1    = "worksflow.qualification-handoff.completion/v1"
	RevisionAuthorityDomainV1 = "worksflow.qualification-handoff.revision-authorities/v1"

	MaximumRetainedBytes = 1 << 20
	MaximumBundleBytes   = 16 << 20
	maximumSafeInteger   = int64(9007199254740991)
)

var (
	ErrInvalid             = errors.New("qualification handoff is invalid")
	ErrNotFound            = errors.New("qualification handoff is not found")
	ErrNotReady            = errors.New("qualification handoff prerequisites are not ready")
	ErrConflict            = errors.New("qualification handoff conflicts with immutable state")
	ErrRetryable           = errors.New("qualification handoff encountered retryable database contention; retry the same handoff")
	ErrStoreOutcomeUnknown = errors.New("qualification handoff store commit outcome is unknown")
	ErrOutcomeUnknown      = errors.New("qualification handoff outcome is unknown; inspect the same handoff")
)

// AtomicStore is the complete database authority surface. Complete must make
// the workflow transition atomically; Inspect must read the immutable result
// by the same identity from a read-write primary.
type AtomicStore interface {
	Complete(context.Context, uuid.UUID) (Record, error)
	Inspect(context.Context, uuid.UUID) (Record, error)
}

type Record struct {
	HandoffID uuid.UUID
	Bundle    CompletionBundle
	// Idempotent is response metadata. It is never included in either retained
	// authority hash. Inspection/recovery sets it true locally.
	Idempotent bool
}

type CompletionBundle struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	HandoffID         string                    `json:"handoffId"`
	Completion        CompletionMaterial        `json:"completion"`
	RevisionAuthority RevisionAuthorityMaterial `json:"revisionAuthority"`
	OutputRevision    OutputRevision            `json:"outputRevision"`
	Workflow          WorkflowProjection        `json:"workflow"`
}

type CompletionMaterial struct {
	Hash     string             `json:"hash"`
	BytesHex string             `json:"bytesHex"`
	Document CompletionDocument `json:"document"`
}

type RevisionAuthorityMaterial struct {
	Hash     string                    `json:"hash"`
	BytesHex string                    `json:"bytesHex"`
	Document RevisionAuthorityDocument `json:"document"`
}

type CompletionDocument struct {
	SchemaVersion             string          `json:"schemaVersion"`
	HandoffID                 string          `json:"handoffId"`
	OperationID               string          `json:"operationId"`
	ConsumptionHash           string          `json:"consumptionHash"`
	OutputRevisionID          string          `json:"outputRevisionId"`
	OutputRevisionContentHash string          `json:"outputRevisionContentHash"`
	ProjectID                 string          `json:"projectId"`
	WorkflowRunID             string          `json:"workflowRunId"`
	NodeRunID                 string          `json:"nodeRunId"`
	NodeKey                   string          `json:"nodeKey"`
	PublishNodeRunID          string          `json:"publishNodeRunId"`
	WorkflowEvents            []WorkflowEvent `json:"workflowEvents"`
	OutboxEvents              []OutboxEvent   `json:"outboxEvents"`
	CompletedAt               string          `json:"completedAt"`
}

type WorkflowEvent struct {
	Role          string `json:"role"`
	EventID       string `json:"eventId"`
	EventSequence int64  `json:"eventSequence"`
	EventType     string `json:"eventType"`
	NodeRunID     string `json:"nodeRunId"`
	NodeKey       string `json:"nodeKey"`
}

type OutboxEvent struct {
	Role            string `json:"role"`
	OutboxEventID   string `json:"outboxEventId"`
	WorkflowEventID string `json:"workflowEventId"`
	EventType       string `json:"eventType"`
}

type RevisionAuthorityDocument struct {
	SchemaVersion          string                 `json:"schemaVersion"`
	HandoffID              string                 `json:"handoffId"`
	OperationID            string                 `json:"operationId"`
	OutputRevisionID       string                 `json:"outputRevisionId"`
	WorkflowInput          AuthorityReference     `json:"workflowInput"`
	Plan                   AuthorityReference     `json:"plan"`
	Receipt                ReceiptReference       `json:"receipt"`
	Promotion              PromotionHashes        `json:"promotion"`
	Target                 PromotionTarget        `json:"target"`
	RevisionStateAtHandoff RevisionStateAtHandoff `json:"revisionStateAtHandoff"`
	CopiedLineage          CopiedLineageSummary   `json:"copiedLineage"`
}

type AuthorityReference struct {
	AuthorityID   string `json:"authorityId"`
	AuthorityHash string `json:"authorityHash"`
}

type ReceiptReference struct {
	ReceiptID    string `json:"receiptId"`
	EnvelopeHash string `json:"envelopeHash"`
}

type PromotionHashes struct {
	RequestHash        string `json:"requestHash"`
	ClosureHash        string `json:"closureHash"`
	RevisionIntentHash string `json:"revisionIntentHash"`
	ConsumptionHash    string `json:"consumptionHash"`
}

type PromotionTarget struct {
	ArtifactID          string `json:"artifactId"`
	NodeKey             string `json:"nodeKey"`
	NodeRunID           string `json:"nodeRunId"`
	ProjectID           string `json:"projectId"`
	RevisionContentHash string `json:"revisionContentHash"`
	RevisionID          string `json:"revisionId"`
	StageGate           string `json:"stageGate"`
	Subject             string `json:"subject"`
	WorkflowRunID       string `json:"workflowRunId"`
}

type RevisionStateAtHandoff struct {
	WorkflowStatus       string  `json:"workflowStatus"`
	ApprovedAt           string  `json:"approvedAt"`
	SupersededAt         *string `json:"supersededAt"`
	ParentWorkflowStatus string  `json:"parentWorkflowStatus"`
	ParentApprovedAt     string  `json:"parentApprovedAt"`
	ParentSupersededAt   string  `json:"parentSupersededAt"`
}

// CopiedLineageSummary is intentionally a fixed-size commitment rather than
// a replay of arbitrarily large lineage rows. Migration 82 freezes the exact
// member ledger separately and binds its Merkle-style domain root here.
//
// Field names are kept in one type so any migration-contract adjustment is a
// compile-visible change rather than an untyped extension map.
type CopiedLineageSummary struct {
	SchemaVersion   string `json:"schemaVersion"`
	RootHash        string `json:"rootHash"`
	SourceCount     int64  `json:"sourceCount"`
	DependencyCount int64  `json:"dependencyCount"`
	TraceCount      int64  `json:"traceCount"`
}

type OutputRevision struct {
	ID                 string               `json:"id"`
	ArtifactID         string               `json:"artifactId"`
	ParentRevisionID   string               `json:"parentRevisionId"`
	RevisionNumber     int64                `json:"revisionNumber"`
	SchemaVersion      int64                `json:"schemaVersion"`
	ContentStore       string               `json:"contentStore"`
	ContentRef         string               `json:"contentRef"`
	ContentHash        string               `json:"contentHash"`
	ByteSize           int64                `json:"byteSize"`
	StateAtHandoff     OutputStateAtHandoff `json:"stateAtHandoff"`
	PromotionHandoffID string               `json:"promotionHandoffId"`
	CreatedAt          string               `json:"createdAt"`
}

type OutputStateAtHandoff struct {
	WorkflowStatus string  `json:"workflowStatus"`
	ApprovedAt     string  `json:"approvedAt"`
	SupersededAt   *string `json:"supersededAt"`
}

type WorkflowProjection struct {
	ProjectID              string        `json:"projectId"`
	WorkflowRunID          string        `json:"workflowRunId"`
	GateNodeRunID          string        `json:"gateNodeRunId"`
	GateNodeKey            string        `json:"gateNodeKey"`
	PublishNodeRunID       string        `json:"publishNodeRunId"`
	PublishNodeKey         string        `json:"publishNodeKey"`
	EventCursorBefore      int64         `json:"eventCursorBefore"`
	EventCursorAfter       int64         `json:"eventCursorAfter"`
	QualityResult          QualityResult `json:"qualityResult"`
	GateStatusAtHandoff    string        `json:"gateStatusAtHandoff"`
	PublishStatusAtHandoff string        `json:"publishStatusAtHandoff"`
	RunStatusAtHandoff     string        `json:"runStatusAtHandoff"`
}

type QualityResult struct {
	Passed            bool              `json:"passed"`
	Findings          QualityFindings   `json:"findings"`
	QualityRunID      string            `json:"qualityRunId"`
	WorkspaceRevision ArtifactReference `json:"workspaceRevision"`
	BuildManifest     BuildManifest     `json:"buildManifest"`
}

type QualityFindings struct {
	Checks            []json.RawMessage `json:"checks"`
	Diagnostics       []json.RawMessage `json:"diagnostics"`
	QualityRunID      string            `json:"qualityRunId"`
	ReportArtifactID  string            `json:"reportArtifactId"`
	ReportRevisionID  string            `json:"reportRevisionId"`
	Score             int64             `json:"score"`
	WorkspaceRevision ArtifactReference `json:"workspaceRevision"`
}

type ArtifactReference struct {
	ArtifactID  string  `json:"artifactId"`
	RevisionID  string  `json:"revisionId"`
	ContentHash string  `json:"contentHash"`
	AnchorID    *string `json:"anchorId,omitempty"`
}

type BuildManifest struct {
	SchemaVersion    int64               `json:"schemaVersion"`
	ProjectID        string              `json:"projectId"`
	RunID            string              `json:"runId"`
	ManifestGroupKey string              `json:"manifestGroupKey"`
	SliceIDs         []string            `json:"sliceIds"`
	BundleIDs        []string            `json:"bundleIds"`
	Sources          []ArtifactReference `json:"sources"`
	Constraints      json.RawMessage     `json:"constraints"`
	CreatedAt        string              `json:"createdAt"`
	Hash             string              `json:"hash"`
}
