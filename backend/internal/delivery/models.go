package delivery

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type qualityRunModel struct {
	ID                   uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID            uuid.UUID  `gorm:"type:uuid;not null;index"`
	WorkflowRunID        *uuid.UUID `gorm:"type:uuid"`
	WorkspaceArtifactID  uuid.UUID  `gorm:"type:uuid;not null"`
	WorkspaceRevisionID  uuid.UUID  `gorm:"type:uuid;not null;index"`
	WorkspaceContentHash string     `gorm:"not null"`
	ReportArtifactID     *uuid.UUID `gorm:"type:uuid"`
	ReportRevisionID     *uuid.UUID `gorm:"type:uuid"`
	BuildArtifactID      *uuid.UUID `gorm:"type:uuid"`
	BuildContentRef      *string
	BuildContentHash     *string
	BuildHash            *string
	BuildEntryPath       *string
	BuildFileCount       *int
	BuildTotalBytes      *int64
	Status               string    `gorm:"not null"`
	Score                int       `gorm:"not null"`
	RunnerVersion        string    `gorm:"not null"`
	SandboxKind          string    `gorm:"not null"`
	Version              uint64    `gorm:"not null"`
	CreatedBy            uuid.UUID `gorm:"type:uuid;not null"`
	StartedAt            time.Time
	CompletedAt          *time.Time
	CreatedAt            time.Time
}

func (qualityRunModel) TableName() string { return "quality_runs" }

type qualityDiagnosticModel struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	QualityRunID uuid.UUID `gorm:"type:uuid;not null;index"`
	CheckID      string    `gorm:"not null"`
	Code         string    `gorm:"not null"`
	Severity     string    `gorm:"not null"`
	Message      string    `gorm:"not null"`
	Path         *string
	Line         *int
	ColumnNumber *int
	Suggestion   *string
	CreatedAt    time.Time
}

func (qualityDiagnosticModel) TableName() string { return "quality_diagnostics" }

type deploymentModel struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID       uuid.UUID  `gorm:"type:uuid;not null;index"`
	Environment     string     `gorm:"not null"`
	EnvironmentRef  string     `gorm:"not null"`
	Provider        string     `gorm:"not null"`
	Status          string     `gorm:"not null"`
	ActiveVersionID *uuid.UUID `gorm:"type:uuid"`
	PublicURL       *string
	Version         uint64 `gorm:"not null"`
	LastError       *string
	CreatedBy       uuid.UUID `gorm:"type:uuid;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (deploymentModel) TableName() string { return "deployments" }

type deploymentVersionModel struct {
	ID                       uuid.UUID  `gorm:"type:uuid;primaryKey"`
	DeploymentID             uuid.UUID  `gorm:"type:uuid;not null;index"`
	Number                   uint64     `gorm:"not null"`
	Action                   string     `gorm:"not null"`
	SourceVersionID          *uuid.UUID `gorm:"type:uuid"`
	WorkspaceArtifactID      uuid.UUID  `gorm:"type:uuid;not null"`
	WorkspaceRevisionID      uuid.UUID  `gorm:"type:uuid;not null;index"`
	WorkspaceContentHash     string     `gorm:"not null"`
	BuildManifestID          *uuid.UUID `gorm:"type:uuid"`
	QualityRunID             *uuid.UUID `gorm:"type:uuid"`
	CanonicalReceiptID       *uuid.UUID `gorm:"type:uuid"`
	CanonicalReceiptHash     *string
	ReleaseBundleID          *uuid.UUID `gorm:"type:uuid"`
	ReleaseBundleHash        *string
	BuildArtifactID          *uuid.UUID `gorm:"type:uuid"`
	BuildContentRef          *string
	BuildContentHash         *string
	BuildHash                *string
	BuildEntryPath           *string
	BuildFileCount           *int
	BuildTotalBytes          *int64
	ProviderRef              string `gorm:"not null"`
	PublicURL                *string
	EntryPath                string          `gorm:"not null"`
	Checksum                 string          `gorm:"not null"`
	FileCount                int             `gorm:"not null"`
	TotalBytes               int64           `gorm:"not null"`
	EnvironmentRef           string          `gorm:"not null"`
	EnvironmentVariableNames json.RawMessage `gorm:"type:jsonb;not null"`
	Status                   string          `gorm:"not null"`
	Message                  string          `gorm:"not null"`
	CreatedBy                uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt                time.Time
}

func (deploymentVersionModel) TableName() string { return "deployment_versions" }

type deploymentLogModel struct {
	ID                  uuid.UUID  `gorm:"type:uuid;primaryKey"`
	DeploymentID        uuid.UUID  `gorm:"type:uuid;not null;index"`
	DeploymentVersionID *uuid.UUID `gorm:"type:uuid"`
	Sequence            uint64     `gorm:"not null"`
	Level               string     `gorm:"not null"`
	Message             string     `gorm:"not null"`
	CreatedAt           time.Time
}

func (deploymentLogModel) TableName() string { return "deployment_logs" }
