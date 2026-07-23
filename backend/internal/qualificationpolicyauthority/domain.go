// Package qualificationpolicyauthority defines the immutable, project-scoped
// policy authority that must be current before Workflow Input can be frozen.
//
// The package is deliberately a pure semantic boundary. It does not read
// Workflow state, approve revisions, resolve independent promotion authorities,
// or expose caller-authored policy JSON. Production adapters are expected to
// implement PolicySource, DatabaseClock, and Store with trusted server-side
// dependencies and transaction locks.
package qualificationpolicyauthority

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

const (
	AuthoritySchemaV1        = "worksflow-qualification-policy-authority/v1"
	RevisionPolicySchemaV1   = "worksflow-qualification-revision-policy/v1"
	PlanInputProfileSchemaV1 = "worksflow-qualification-plan-input-profile/v1"
	PromotionPolicySchemaV1  = "worksflow-qualification-promotion-policy/v1"

	AuthorityHashPrefixV1        = "worksflow-qualification-policy-authority-hash/v1"
	RevisionPolicyHashDomainV1   = "worksflow.qualification-policy.revision/v1"
	PlanInputProfileHashDomainV1 = "worksflow.qualification-policy.plan-input-profile/v1"
	PromotionPolicyHashDomainV1  = "worksflow.qualification-policy.promotion/v1"
	AuthorityHashDomainV1        = "worksflow.qualification-policy.authority/v1"

	ExecutionProfileV3       = "workflow-engine/v3"
	ExternalGatePolicyV1     = "external-qualification/v1"
	SupersessionPolicyV1     = "invalidate-unconsumed/v1"
	AuthorityStatusActive    = "active"
	AuthorityStatusSuspended = "suspended"
	CurrencyLatestApproved   = "latest-approved-required"
	CurrencyExactApproved    = "exact-approved"
	WorkspaceSourceKind      = "workspace"
	WorkspaceRevisionPurpose = "workspace-target"
	ChangeSourceAIProposal   = "ai_proposal"
	ChangeSourceHuman        = "human"
	ChangeSourceImport       = "import"
	ChangeSourceMerge        = "merge"
	ChangeSourceRollback     = "rollback"
	ChangeSourceSystem       = "system"

	QualificationPlanAuthoritySchemaV1 = "worksflow-qualification-plan-authority/v1"
	QualificationReceiptSchemaV3       = "worksflow-qualification-receipt/v3"
	QualificationPromotionProtocolV2   = "worksflow-qualification-promotion-consume/v2"
	IndependentModelProfileActivation  = "model-profile-activation"
	IndependentProductionPostgres      = "production-postgresql-posture"

	CredentialRevocationPolicyV1 = "exact-issued-set-before-index/v1"
	PlaintextDispositionPolicyV1 = "restricted-plaintext-disposed-before-kms/v1"

	MaximumExactApprovedSources    = 2048
	MaximumIndependentRequirements = 2
	MaximumCanonicalBytes          = 16 << 20
	MaximumAuthorityBytes          = 32 << 20
	MaximumJavaScriptSafeInteger   = int64(9007199254740991)
)

var (
	ErrInvalid             = errors.New("qualification policy authority input is invalid")
	ErrNotFound            = errors.New("qualification policy authority is not found")
	ErrConflict            = errors.New("qualification policy authority conflicts with immutable state")
	ErrStale               = errors.New("qualification policy authority is not current and active")
	ErrStoreOutcomeUnknown = errors.New("qualification policy authority store commit outcome is unknown")
	ErrOutcomeUnknown      = errors.New("qualification policy authority outcome is unknown; inspect the same operation")
)

// IssueCommand is intentionally opaque. Policy content and all policy switches
// are resolved from a trusted PolicySource and cannot be supplied by an API
// client. ExpectedPreviousAuthorityHash is empty only for the first generation.
type IssueCommand struct {
	OperationID                   uuid.UUID
	AuthorityID                   uuid.UUID
	PolicySourceID                string
	ExpectedPreviousAuthorityHash string
}

type ExecutionProfileBinding struct {
	Hash    string `json:"hash"`
	Version string `json:"version"`
}

type WorkspaceTargetPolicy struct {
	CanonicalReviewRequired bool   `json:"canonicalReviewRequired"`
	CurrencyPolicy          string `json:"currencyPolicy"`
}

type ChangeSourceReviewRule struct {
	CanonicalReviewRequired bool   `json:"canonicalReviewRequired"`
	ChangeSource            string `json:"changeSource"`
}

// ExactApprovedSource is the only v1 escape from latest-approved currency. It
// is deliberately a complete immutable tuple and never changes review policy.
type ExactApprovedSource struct {
	ArtifactID  string `json:"artifactId"`
	ContentHash string `json:"contentHash"`
	Purpose     string `json:"purpose"`
	RevisionID  string `json:"revisionId"`
	SourceKind  string `json:"sourceKind"`
}

type RevisionPolicy struct {
	ExactApprovedSources []ExactApprovedSource    `json:"exactApprovedSources"`
	ReviewByChangeSource []ChangeSourceReviewRule `json:"reviewByChangeSource"`
	SchemaVersion        string                   `json:"schemaVersion"`
	SourceCurrencyPolicy string                   `json:"sourceCurrencyPolicy"`
	WorkspaceTarget      WorkspaceTargetPolicy    `json:"workspaceTarget"`
}

type ArtifactPolicy struct {
	MaximumArtifacts            int  `json:"maximumArtifacts"`
	RequireRestrictedEncryption bool `json:"requireRestrictedEncryption"`
	RequireTrace                bool `json:"requireTrace"`
	RequireVideo                bool `json:"requireVideo"`
}

type ArtifactExpectation struct {
	Classification qualificationevidence.Classification `json:"classification"`
	ID             string                               `json:"id"`
	Kind           qualificationevidence.ArtifactKind   `json:"kind"`
}

type OutputPolicy struct {
	CredentialRevocation string `json:"credentialRevocation"`
	PlaintextDisposition string `json:"plaintextDisposition"`
	SnapshotMode         string `json:"snapshotMode"`
}

type QualificationManifestBinding struct {
	ArtifactID  string `json:"artifactId"`
	ContentHash string `json:"contentHash"`
	PlanDigest  string `json:"planDigest"`
	RevisionID  string `json:"revisionId"`
}

// CredentialProfile identifies the trusted precommit resolver. It contains no
// credential set, handle, member, token, or other runtime secret value.
type CredentialProfile struct {
	Audience               string `json:"audience"`
	AuthorityID            string `json:"authorityId"`
	IssuanceArtifactID     string `json:"issuanceArtifactId"`
	MemberRequestSetDigest string `json:"memberRequestSetDigest"`
	RevocationArtifactID   string `json:"revocationArtifactId"`
}

type PlanInputProfile struct {
	ArtifactPolicy        ArtifactPolicy                              `json:"artifactPolicy"`
	Artifacts             []ArtifactExpectation                       `json:"artifacts"`
	CredentialProfile     CredentialProfile                           `json:"credentialProfile"`
	GoldenRuntime         qualificationreceipt.GoldenRuntimeBinding   `json:"goldenRuntime"`
	OutputPolicy          OutputPolicy                                `json:"outputPolicy"`
	Outputs               qualificationevidence.OutputExpectation     `json:"outputs"`
	QualificationManifest QualificationManifestBinding                `json:"qualificationManifest"`
	Recipient             qualificationevidence.EncryptionRecipient   `json:"recipient"`
	SchemaVersion         string                                      `json:"schemaVersion"`
	SourcePolicyDigest    string                                      `json:"sourcePolicyDigest"`
	TemplateRelease       qualificationreceipt.TemplateReleaseBinding `json:"templateRelease"`
	TrustBindings         qualificationevidence.TrustBindings         `json:"trustBindings"`
	TrustPolicyDigest     string                                      `json:"trustPolicyDigest"`
}

// IndependentAuthorityBinding remains deliberately opaque. This package only
// freezes the reviewed ID/hash requirement; it does not invent a durable
// ModelProfile or PostgreSQL-posture authority or a resolver for either kind.
type IndependentAuthorityBinding struct {
	AuthorityHash string `json:"authorityHash"`
	AuthorityID   string `json:"authorityId"`
	Kind          string `json:"kind"`
}

type PromotionPolicy struct {
	IndependentRequirements []IndependentAuthorityBinding `json:"independentRequirements"`
	PlanAuthoritySchema     string                        `json:"planAuthoritySchema"`
	ReceiptSchema           string                        `json:"receiptSchema"`
	SchemaVersion           string                        `json:"schemaVersion"`
	SingleUseProtocol       string                        `json:"singleUseProtocol"`
}

// ResolvedPolicy is returned only by a trusted server-installed PolicySource.
// Issuance identity, generation, previous hash, and database time are assigned
// by Service and Store rather than accepted from the resolver.
type ResolvedPolicy struct {
	ExecutionProfile   ExecutionProfileBinding
	ExternalGatePolicy string
	PlanInputProfile   PlanInputProfile
	ProjectID          uuid.UUID
	PromotionPolicy    PromotionPolicy
	RevisionPolicy     RevisionPolicy
	Status             string
	SupersessionPolicy string
}

type ComponentDigests struct {
	PlanInputProfile string `json:"planInputProfile"`
	PromotionPolicy  string `json:"promotionPolicy"`
	RevisionPolicy   string `json:"revisionPolicy"`
}

// AuthorityDocument is the complete immutable v1 root. AuthorityHash is
// intentionally not a member of the document it authenticates.
type AuthorityDocument struct {
	AuthorityID           string                  `json:"authorityId"`
	ComponentDigests      ComponentDigests        `json:"componentDigests"`
	ExecutionProfile      ExecutionProfileBinding `json:"executionProfile"`
	ExternalGatePolicy    string                  `json:"externalGatePolicy"`
	Generation            int64                   `json:"generation"`
	IssuedAt              string                  `json:"issuedAt"`
	OperationID           string                  `json:"operationId"`
	PlanInputProfile      PlanInputProfile        `json:"planInputProfile"`
	PolicySourceID        string                  `json:"policySourceId"`
	PreviousAuthorityHash *string                 `json:"previousAuthorityHash"`
	ProjectID             string                  `json:"projectId"`
	PromotionPolicy       PromotionPolicy         `json:"promotionPolicy"`
	RevisionPolicy        RevisionPolicy          `json:"revisionPolicy"`
	SchemaVersion         string                  `json:"schemaVersion"`
	Status                string                  `json:"status"`
	SupersessionPolicy    string                  `json:"supersessionPolicy"`
}

// Record retains exact canonical bytes for independent cross-checking.
// Idempotent is response metadata and is excluded from immutable comparison.
type Record struct {
	Command IssueCommand

	RevisionPolicy      RevisionPolicy
	RevisionPolicyBytes []byte
	RevisionPolicyHash  string

	PlanInputProfile      PlanInputProfile
	PlanInputProfileBytes []byte
	PlanInputProfileHash  string

	PromotionPolicy      PromotionPolicy
	PromotionPolicyBytes []byte
	PromotionPolicyHash  string

	Document      AuthorityDocument
	DocumentBytes []byte
	AuthorityHash string
	IssuedAt      time.Time
	Idempotent    bool
}

type PolicySource interface {
	Resolve(context.Context, string) (ResolvedPolicy, error)
}

// DatabaseClock abstracts SELECT clock_timestamp() (or an equivalent trusted
// database time source) and must return canonical millisecond precision.
type DatabaseClock interface {
	Now(context.Context) (time.Time, error)
}

type DatabaseClockFunc func(context.Context) (time.Time, error)

func (function DatabaseClockFunc) Now(ctx context.Context) (time.Time, error) {
	return function(ctx)
}

// Store atomically appends one generation after comparing the current head.
// ResolveCurrent is diagnostic; mutation consumers must use AssertCurrent in
// their own production transaction adapter.
type Store interface {
	Issue(context.Context, Record) (Record, error)
	InspectOperation(context.Context, uuid.UUID) (Record, error)
	ResolveAuthority(context.Context, uuid.UUID) (Record, error)
	ResolveCurrent(context.Context, uuid.UUID, ExecutionProfileBinding) (Record, error)
	AssertCurrent(context.Context, uuid.UUID) (Record, error)
}
