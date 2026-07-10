package designimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	MaxUploadBytes              int64 = 8 << 20
	MaxRequestBytes             int64 = 12 << 20
	defaultSnapshotContentBytes int64 = 16 << 20
	snapshotEnvelopeReserve     int64 = 512 << 10
)

var (
	ErrInvalidInput          = errors.New("design import input is invalid")
	ErrUnsupportedMediaType  = errors.New("design import media type is unsupported")
	ErrUploadTooLarge        = errors.New("design import upload is too large")
	ErrCapabilityUnavailable = errors.New("design import capability is unavailable")
	ErrConflict              = errors.New("design import changed since it was loaded")
	ErrProcessing            = errors.New("design import creation is already being processed")
)

type SourceKind string

const (
	SourceFigma      SourceKind = "figma"
	SourcePenpot     SourceKind = "penpot"
	SourceExcalidraw SourceKind = "excalidraw"
	SourceTLDraw     SourceKind = "tldraw"
	SourceStorybook  SourceKind = "storybook"
	SourceLadle      SourceKind = "ladle"
	SourceUpload     SourceKind = "upload"
)

type SourceCapability struct {
	SourceKind       SourceKind `json:"sourceKind"`
	Label            string     `json:"label"`
	UploadEnabled    bool       `json:"uploadEnabled"`
	UploadReason     string     `json:"uploadReason,omitempty"`
	RemoteEnabled    bool       `json:"remoteEnabled"`
	RemoteReason     string     `json:"remoteReason,omitempty"`
	AcceptedMedia    []string   `json:"acceptedMediaTypes"`
	AcceptedSuffixes []string   `json:"acceptedFileExtensions"`
	MaxUploadBytes   int64      `json:"maxUploadBytes"`
}

type Capabilities struct {
	SnapshotPolicy string             `json:"snapshotPolicy"`
	TrustPolicy    string             `json:"trustPolicy"`
	Sources        []SourceCapability `json:"sources"`
}

type UploadFile struct {
	Name          string `json:"name"`
	MediaType     string `json:"mediaType"`
	ContentBase64 string `json:"contentBase64"`
}

type CreateInput struct {
	SourceKind                SourceKind      `json:"sourceKind"`
	Mode                      string          `json:"mode"`
	Title                     string          `json:"title,omitempty"`
	SourceURL                 string          `json:"sourceUrl,omitempty"`
	File                      *UploadFile     `json:"file,omitempty"`
	SelectedFrameIDs          []string        `json:"selectedFrameIds,omitempty"`
	PageSpecRevision          core.VersionRef `json:"pageSpecRevision"`
	TargetPrototypeArtifactID string          `json:"targetPrototypeArtifactId,omitempty"`
}

type DecisionInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	Version  uint64 `json:"version"`
}

type Snapshot struct {
	ContentHash    string     `json:"contentHash"`
	RawContentHash string     `json:"rawContentHash"`
	SourceKind     SourceKind `json:"sourceKind"`
	SourceName     string     `json:"sourceName"`
	Mode           string     `json:"mode"`
	SourceURL      string     `json:"sourceUrl,omitempty"`
	FileName       string     `json:"fileName,omitempty"`
	MediaType      string     `json:"mediaType"`
	ByteSize       int64      `json:"byteSize"`
	CapturedAt     time.Time  `json:"capturedAt"`
	SelectedFrames []string   `json:"selectedFrameIds"`
}

type Import struct {
	ID                  string                 `json:"id"`
	ProjectID           string                 `json:"projectId"`
	Status              string                 `json:"status"`
	PipelineStage       string                 `json:"pipelineStage"`
	Version             uint64                 `json:"version"`
	ETag                string                 `json:"etag"`
	Snapshot            Snapshot               `json:"snapshot"`
	PageSpecRevision    core.VersionRef        `json:"pageSpecRevision"`
	PrototypeArtifactID string                 `json:"prototypeArtifactId,omitempty"`
	BaseRevisionID      string                 `json:"baseRevisionId,omitempty"`
	InputManifestID     string                 `json:"inputManifestId,omitempty"`
	OutputProposalID    string                 `json:"outputProposalId,omitempty"`
	OperationID         string                 `json:"operationId,omitempty"`
	AppliedRevisionID   string                 `json:"appliedRevisionId,omitempty"`
	CreatesPrototype    bool                   `json:"createsPrototype"`
	FailureCode         string                 `json:"failureCode,omitempty"`
	FailureDetail       string                 `json:"failureDetail,omitempty"`
	CreatedBy           string                 `json:"createdBy"`
	CreatedAt           time.Time              `json:"createdAt"`
	UpdatedAt           time.Time              `json:"updatedAt"`
	DecidedBy           string                 `json:"decidedBy,omitempty"`
	DecidedAt           *time.Time             `json:"decidedAt,omitempty"`
	Manifest            *domain.InputManifest  `json:"manifest,omitempty"`
	Proposal            *domain.OutputProposal `json:"proposal,omitempty"`
}

type CatalogItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Count int    `json:"count,omitempty"`
}

type ImportCatalog struct {
	Pages            []CatalogItem `json:"pages"`
	Components       []CatalogItem `json:"components"`
	States           []CatalogItem `json:"states"`
	Interactions     []CatalogItem `json:"interactions"`
	Truncated        bool          `json:"truncated"`
	TruncationReason string        `json:"truncationReason,omitempty"`
}

type SnapshotEnvelope struct {
	SchemaVersion    int             `json:"schemaVersion"`
	SourceKind       SourceKind      `json:"sourceKind"`
	SourceName       string          `json:"sourceName"`
	MediaType        string          `json:"mediaType"`
	FileName         string          `json:"fileName"`
	ByteSize         int64           `json:"byteSize"`
	RawContentHash   string          `json:"rawContentHash"`
	ContentBase64    string          `json:"contentBase64"`
	CapturedAt       time.Time       `json:"capturedAt"`
	ExtractedCatalog ImportCatalog   `json:"extractedCatalog"`
	Safety           json.RawMessage `json:"safety"`
}

type Error struct {
	Kind   error
	Field  string
	Detail string
}

func (e *Error) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Kind, e.Detail)
	}
	return fmt.Sprintf("%s (%s): %s", e.Kind, e.Field, e.Detail)
}

func (e *Error) Unwrap() error { return e.Kind }

func invalid(field, detail string) error {
	return &Error{Kind: ErrInvalidInput, Field: field, Detail: detail}
}

func mediaError(field, detail string) error {
	return &Error{Kind: ErrUnsupportedMediaType, Field: field, Detail: detail}
}
