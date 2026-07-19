package constructor

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type contractModel struct {
	ID                    uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID             uuid.UUID `gorm:"type:uuid;not null;index"`
	BuildManifestID       uuid.UUID `gorm:"type:uuid;not null;index"`
	BuildManifestHash     string    `gorm:"not null"`
	FullStackTemplateID   uuid.UUID `gorm:"type:uuid;not null"`
	FullStackTemplateHash string    `gorm:"not null"`
	SchemaVersion         string    `gorm:"not null"`
	CompilerVersion       string    `gorm:"not null"`
	CompilerHash          string    `gorm:"not null"`
	ContentStore          string    `gorm:"not null"`
	ContentRef            string    `gorm:"not null"`
	ContentHash           string    `gorm:"not null"`
	ContractHash          string    `gorm:"not null"`
	Status                string    `gorm:"not null"`
	MustCount             int       `gorm:"not null"`
	MustReadyCount        int       `gorm:"not null"`
	ObligationCount       int       `gorm:"not null"`
	SourceCount           int       `gorm:"not null"`
	TemplateReleaseCount  int       `gorm:"not null"`
	BlockingCount         int       `gorm:"not null"`
	ConflictCount         int       `gorm:"not null"`
	Version               uint64    `gorm:"not null"`
	CreatedBy             uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt             time.Time
	SupersededAt          *time.Time
}

func (contractModel) TableName() string { return "application_build_contracts" }

type contractSourceModel struct {
	ContractID  uuid.UUID `gorm:"type:uuid;primaryKey"`
	Ordinal     int       `gorm:"primaryKey"`
	SourceKind  string    `gorm:"not null"`
	Purpose     string    `gorm:"not null"`
	Required    bool      `gorm:"not null"`
	ArtifactID  uuid.UUID `gorm:"type:uuid;not null"`
	RevisionID  uuid.UUID `gorm:"type:uuid;not null"`
	ContentHash string    `gorm:"not null"`
}

func (contractSourceModel) TableName() string { return "application_build_contract_sources" }

type contractTemplateReleaseModel struct {
	ContractID                 uuid.UUID `gorm:"type:uuid;primaryKey"`
	Ordinal                    int       `gorm:"primaryKey"`
	Role                       string    `gorm:"not null"`
	TemplateReleaseID          uuid.UUID `gorm:"type:uuid;not null"`
	TemplateReleaseContentHash string    `gorm:"not null"`
}

func (contractTemplateReleaseModel) TableName() string {
	return "application_build_contract_template_releases"
}

type contractObligationModel struct {
	ContractID        uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ObligationID      string          `gorm:"primaryKey"`
	Level             string          `gorm:"not null"`
	Kind              string          `gorm:"not null"`
	SourceArtifactID  uuid.UUID       `gorm:"type:uuid;not null"`
	SourceRevisionID  uuid.UUID       `gorm:"type:uuid;not null"`
	SourceContentHash string          `gorm:"not null"`
	SourceAnchorID    string          `gorm:"not null"`
	OracleIDs         json.RawMessage `gorm:"type:jsonb;not null"`
	DependsOn         json.RawMessage `gorm:"type:jsonb;not null"`
	Waivable          bool            `gorm:"not null"`
	Status            string          `gorm:"not null"`
	BlockingReasonID  *string
}

func (contractObligationModel) TableName() string {
	return "application_build_contract_obligations"
}
