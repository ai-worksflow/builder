package designimport

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type importModel struct {
	ID                          uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID                   uuid.UUID `gorm:"type:uuid;not null;index"`
	SourceKind                  string    `gorm:"not null"`
	SourceMode                  string    `gorm:"not null"`
	SourceName                  string    `gorm:"not null"`
	SourceURL                   *string
	FileName                    *string
	MediaType                   string          `gorm:"not null"`
	ByteSize                    int64           `gorm:"not null"`
	RawContentHash              string          `gorm:"not null"`
	SnapshotStore               string          `gorm:"not null"`
	SnapshotRef                 string          `gorm:"not null"`
	SnapshotContentHash         string          `gorm:"not null"`
	SnapshotSchemaVersion       int             `gorm:"not null"`
	SelectedFrameIDs            json.RawMessage `gorm:"type:jsonb;not null"`
	PageSpecArtifactID          uuid.UUID       `gorm:"type:uuid;not null"`
	PageSpecRevisionID          uuid.UUID       `gorm:"type:uuid;not null"`
	PageSpecContentHash         string          `gorm:"not null"`
	CreatesPrototype            bool            `gorm:"not null"`
	ExpectedPrototypeArtifactID uuid.UUID       `gorm:"type:uuid;not null"`
	ExpectedBaseRevisionID      uuid.UUID       `gorm:"type:uuid;not null"`
	ExpectedInputManifestID     uuid.UUID       `gorm:"type:uuid;not null"`
	ExpectedOutputProposalID    uuid.UUID       `gorm:"type:uuid;not null"`
	PrototypeArtifactID         *uuid.UUID      `gorm:"type:uuid"`
	BaseRevisionID              *uuid.UUID      `gorm:"type:uuid"`
	InputManifestID             *uuid.UUID      `gorm:"type:uuid"`
	OutputProposalID            *uuid.UUID      `gorm:"type:uuid"`
	OperationID                 *string
	AppliedRevisionID           *uuid.UUID `gorm:"type:uuid"`
	PipelineStage               string     `gorm:"not null"`
	CreateClaimToken            *uuid.UUID `gorm:"type:uuid"`
	CreateClaimedBy             *uuid.UUID `gorm:"type:uuid"`
	CreateClaimedAt             *time.Time
	CreateClaimExpiresAt        *time.Time
	Status                      string `gorm:"not null"`
	FailureCode                 *string
	FailureDetail               *string
	RequestKeyHash              string     `gorm:"not null"`
	Version                     uint64     `gorm:"not null"`
	CreatedBy                   uuid.UUID  `gorm:"type:uuid;not null"`
	DecidedBy                   *uuid.UUID `gorm:"type:uuid"`
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	DecidedAt                   *time.Time
}

func (importModel) TableName() string { return "design_imports" }
