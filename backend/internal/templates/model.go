package templates

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type templateArtifactAuthorityReceiptModel struct {
	ID                       uuid.UUID       `gorm:"type:uuid;primaryKey"`
	SchemaVersion            string          `gorm:"not null"`
	Decision                 string          `gorm:"not null"`
	SubjectHash              string          `gorm:"not null"`
	SourceTreeHash           string          `gorm:"not null"`
	ArtifactDigest           string          `gorm:"not null"`
	SBOMDigest               string          `gorm:"not null"`
	SignatureBundleDigest    string          `gorm:"not null"`
	PolicyHash               string          `gorm:"not null"`
	ContentHash              string          `gorm:"not null"`
	AuthorityID              string          `gorm:"not null"`
	AuthorityVersion         string          `gorm:"not null"`
	VerifierImageDigest      string          `gorm:"not null"`
	TrustRootDigest          string          `gorm:"not null"`
	TransparencyLogID        string          `gorm:"not null"`
	TransparencyEntryUUID    string          `gorm:"not null"`
	TransparencyLogIndex     int64           `gorm:"not null"`
	TransparencyBundleDigest string          `gorm:"not null"`
	TransparencyTreeSize     int64           `gorm:"not null"`
	TransparencyRootHash     string          `gorm:"not null"`
	IntegratedAt             time.Time       `gorm:"not null"`
	VerificationReference    string          `gorm:"not null"`
	VerifiedAt               time.Time       `gorm:"not null"`
	RecordedBy               uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt                time.Time       `gorm:"not null"`
	Document                 json.RawMessage `gorm:"type:jsonb;not null"`
}

func (templateArtifactAuthorityReceiptModel) TableName() string {
	return "template_artifact_authority_receipts"
}

type admissionAttemptModel struct {
	ID                          uuid.UUID        `gorm:"type:uuid;primaryKey"`
	SchemaVersion               string           `gorm:"not null"`
	Status                      string           `gorm:"not null"`
	Version                     uint64           `gorm:"not null"`
	Source                      json.RawMessage  `gorm:"type:jsonb;not null"`
	Manifest                    json.RawMessage  `gorm:"type:jsonb;not null"`
	SBOMDigest                  string           `gorm:"not null"`
	LicenseExpression           string           `gorm:"not null"`
	LicenseDigest               string           `gorm:"not null"`
	SubjectHash                 string           `gorm:"not null"`
	Evidence                    json.RawMessage  `gorm:"type:jsonb;not null"`
	Signature                   *json.RawMessage `gorm:"type:jsonb"`
	Findings                    json.RawMessage  `gorm:"type:jsonb;not null"`
	ApprovedReleaseID           *uuid.UUID       `gorm:"type:uuid"`
	RequestedBy                 uuid.UUID        `gorm:"type:uuid;not null"`
	EvaluatedBy                 *uuid.UUID       `gorm:"type:uuid"`
	CreatedAt                   time.Time        `gorm:"not null"`
	UpdatedAt                   time.Time        `gorm:"not null"`
	EvaluatedAt                 *time.Time
	AuthorityReceiptID          *uuid.UUID `gorm:"type:uuid"`
	AuthorityReceiptContentHash *string
	AuthorityPolicyHash         *string
}

func (admissionAttemptModel) TableName() string { return "template_admission_attempts" }

type templateReleaseModel struct {
	ID                          uuid.UUID       `gorm:"type:uuid;primaryKey"`
	SchemaVersion               string          `gorm:"not null"`
	AdmissionAttemptID          uuid.UUID       `gorm:"type:uuid;not null"`
	TemplateID                  string          `gorm:"not null"`
	ReleaseVersion              string          `gorm:"not null"`
	SourceRepository            string          `gorm:"not null"`
	SourceBranch                string          `gorm:"not null"`
	SourceCommit                string          `gorm:"not null"`
	TreeHash                    string          `gorm:"not null"`
	Manifest                    json.RawMessage `gorm:"type:jsonb;not null"`
	SBOMDigest                  string          `gorm:"not null"`
	LicenseExpression           string          `gorm:"not null"`
	LicenseDigest               string          `gorm:"not null"`
	EvidenceRefs                json.RawMessage `gorm:"type:jsonb;not null"`
	Signature                   json.RawMessage `gorm:"type:jsonb;not null"`
	SubjectHash                 string          `gorm:"not null"`
	ContentHash                 string          `gorm:"not null"`
	ApprovedBy                  uuid.UUID       `gorm:"type:uuid;not null"`
	ApprovedAt                  time.Time       `gorm:"not null"`
	CreatedAt                   time.Time       `gorm:"not null"`
	AuthorityReceiptID          *uuid.UUID      `gorm:"type:uuid"`
	AuthorityReceiptContentHash *string
	AuthorityPolicyHash         *string
}

func (templateReleaseModel) TableName() string { return "template_releases" }

type templateReleasePolicyModel struct {
	SchemaVersion               string     `gorm:"not null"`
	TemplateReleaseID           uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ReleaseContentHash          string     `gorm:"not null"`
	State                       string     `gorm:"not null"`
	Version                     uint64     `gorm:"not null"`
	Reason                      string     `gorm:"not null"`
	UpdatedBy                   uuid.UUID  `gorm:"type:uuid;not null"`
	CreatedAt                   time.Time  `gorm:"not null"`
	UpdatedAt                   time.Time  `gorm:"not null"`
	AuthorityReceiptID          *uuid.UUID `gorm:"type:uuid"`
	AuthorityReceiptContentHash *string
	AuthorityPolicyHash         *string
}

func (templateReleasePolicyModel) TableName() string { return "template_release_policies" }

type fullStackTemplateReleaseModel struct {
	ID             uuid.UUID       `gorm:"type:uuid;primaryKey"`
	SchemaVersion  string          `gorm:"not null"`
	TemplateID     string          `gorm:"not null"`
	ReleaseVersion string          `gorm:"not null"`
	Document       json.RawMessage `gorm:"type:jsonb;not null"`
	ContentHash    string          `gorm:"not null"`
	CreatedBy      uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt      time.Time       `gorm:"not null"`
}

func (fullStackTemplateReleaseModel) TableName() string { return "full_stack_template_releases" }

type fullStackTemplateComponentModel struct {
	FullStackTemplateID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	FullStackContentHash       string    `gorm:"not null"`
	Role                       string    `gorm:"primaryKey"`
	MountPath                  string    `gorm:"not null"`
	TemplateReleaseID          uuid.UUID `gorm:"type:uuid;not null"`
	TemplateReleaseContentHash string    `gorm:"not null"`
}

func (fullStackTemplateComponentModel) TableName() string {
	return "full_stack_template_components"
}
