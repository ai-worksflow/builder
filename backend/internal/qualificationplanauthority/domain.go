// Package qualificationplanauthority freezes a server-resolved qualification
// input into one immutable qualificationevidence.Plan authority envelope.
//
// This package closes caller Plan self-certification only. It neither verifies
// evidence nor authorizes or performs qualification promotion.
package qualificationplanauthority

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

const (
	FreezeRequestSchemaV1 = "worksflow-qualification-plan-freeze-request/v1"
	InputSchemaV1         = "worksflow-qualification-plan-input/v1"
	TrustSchemaV1         = "worksflow-qualification-plan-trust/v1"
	TargetSchemaV1        = "worksflow-qualification-plan-target/v1"
	AuthoritySchemaV1     = "worksflow-qualification-plan-authority/v1"

	QualificationPlanArtifactPrefix = "qualification-plan-"
	CredentialRevocationPolicyV1    = "exact-issued-set-before-index/v1"
	PlaintextDispositionPolicyV1    = "restricted-plaintext-disposed-before-kms/v1"
)

var (
	ErrInvalid             = errors.New("qualification plan authority input is invalid")
	ErrNotFound            = errors.New("qualification plan authority is not found")
	ErrConflict            = errors.New("qualification plan authority identity conflicts with immutable state")
	ErrOutcomeUnknown      = errors.New("qualification plan authority outcome is unknown; inspect the same operation")
	ErrStoreOutcomeUnknown = errors.New("qualification plan authority store commit outcome is unknown")
)

// FreezeCommand is intentionally opaque. Every executable or policy input is
// resolved from trusted server storage rather than copied from a browser DTO.
type FreezeCommand struct {
	OperationID      uuid.UUID
	AuthorityID      uuid.UUID
	InputAuthorityID uuid.UUID
}

type FreezeRequest struct {
	AuthorityID      string `json:"authorityId"`
	InputAuthorityID string `json:"inputAuthorityId"`
	OperationID      string `json:"operationId"`
	SchemaVersion    string `json:"schemaVersion"`
}

type ArtifactRevisionBinding struct {
	ArtifactID  string `json:"artifactId"`
	ContentHash string `json:"contentHash"`
	RevisionID  string `json:"revisionId"`
}

type ImmutableContentBinding struct {
	ContentHash string `json:"contentHash"`
	ID          string `json:"id"`
}

// ArtifactPolicy is closed and fail-safe. It prevents an input authority from
// silently weakening the evidence package while retaining a valid input hash.
type ArtifactPolicy struct {
	MaximumArtifacts            int  `json:"maximumArtifacts"`
	RequireRestrictedEncryption bool `json:"requireRestrictedEncryption"`
	RequireTrace                bool `json:"requireTrace"`
	RequireVideo                bool `json:"requireVideo"`
}

// ArtifactExpectation deliberately omits EncryptionOperationID. The plan
// authority allocates that operation identity for every restricted artifact.
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

// ResolvedInputDocument is the complete immutable server-owned input. The
// qualification manifest projection is retained as a separate exact raw byte
// sequence because its established schema is intentionally richer than this
// orchestration package.
type ResolvedInputDocument struct {
	ArtifactPolicy          ArtifactPolicy                              `json:"artifactPolicy"`
	Artifacts               []ArtifactExpectation                       `json:"artifacts"`
	BuildContract           ImmutableContentBinding                     `json:"buildContract"`
	BuildManifest           ImmutableContentBinding                     `json:"buildManifest"`
	Credential              qualificationevidence.CredentialExpectation `json:"credential"`
	GoldenRuntime           qualificationreceipt.GoldenRuntimeBinding   `json:"goldenRuntime"`
	Outputs                 qualificationevidence.OutputExpectation     `json:"outputs"`
	OutputPolicy            OutputPolicy                                `json:"outputPolicy"`
	PromotionTarget         qualificationreceipt.PromotionTarget        `json:"promotionTarget"`
	QualificationManifest   ArtifactRevisionBinding                     `json:"qualificationManifest"`
	QualificationPlanDigest string                                      `json:"qualificationPlanDigest"`
	Recipient               qualificationevidence.EncryptionRecipient   `json:"recipient"`
	SchemaVersion           string                                      `json:"schemaVersion"`
	Source                  qualificationreceipt.SourceBinding          `json:"source"`
	TemplateRelease         qualificationreceipt.TemplateReleaseBinding `json:"templateRelease"`
	TrustBindings           qualificationevidence.TrustBindings         `json:"trustBindings"`
	TrustPolicyDigest       string                                      `json:"trustPolicyDigest"`
}

// ResolvedInputs carries exact canonical bytes in addition to the typed
// document. QualificationPlanBytes are the canonical
// worksflow-qualification-plan/v1 projection whose digest is
// Input.QualificationPlanDigest.
type ResolvedInputs struct {
	Input                  ResolvedInputDocument
	InputHash              string
	InputBytes             []byte
	QualificationPlanBytes []byte
}

type InputAuthority interface {
	Resolve(context.Context, uuid.UUID) (ResolvedInputs, error)
}

type TrustDocument struct {
	SchemaVersion     string                              `json:"schemaVersion"`
	TrustBindings     qualificationevidence.TrustBindings `json:"trustBindings"`
	TrustPolicyDigest string                              `json:"trustPolicyDigest"`
}

type TargetDocument struct {
	PromotionTarget qualificationreceipt.PromotionTarget `json:"promotionTarget"`
	SchemaVersion   string                               `json:"schemaVersion"`
}

// AuthorityEnvelope binds all separately retained authorities without
// recursively embedding AuthorityHash.
type AuthorityEnvelope struct {
	ArtifactID          string `json:"artifactId"`
	AuthorityID         string `json:"authorityId"`
	EvidencePlanHash    string `json:"evidencePlanHash"`
	InputAuthorityID    string `json:"inputAuthorityId"`
	InputHash           string `json:"inputHash"`
	ManifestPlanDigest  string `json:"manifestPlanDigest"`
	OperationID         string `json:"operationId"`
	ProjectionHash      string `json:"projectionHash"`
	SchemaVersion       string `json:"schemaVersion"`
	TargetHash          string `json:"targetHash"`
	TrustBindingsDigest string `json:"trustBindingsDigest"`
	TrustHash           string `json:"trustHash"`
}

// Record retains every canonical byte authority needed for independent SQL
// cross-checking and deterministic replay. FrozenAt is assigned by Store, not
// by InputAuthority or the caller.
type Record struct {
	OperationID      uuid.UUID
	AuthorityID      uuid.UUID
	InputAuthorityID uuid.UUID

	RequestHash  string
	RequestBytes []byte
	Request      FreezeRequest

	InputHash  string
	InputBytes []byte
	Input      ResolvedInputDocument

	ProjectionHash     string
	ProjectionBytes    []byte
	ProjectionDocument json.RawMessage

	EvidencePlanHash  string
	EvidencePlanBytes []byte
	EvidencePlan      qualificationevidence.Plan

	TrustHash  string
	TrustBytes []byte
	Trust      TrustDocument

	TargetHash  string
	TargetBytes []byte
	Target      TargetDocument

	EnvelopeHash  string
	EnvelopeBytes []byte
	Envelope      AuthorityEnvelope

	FrozenAt   time.Time
	Idempotent bool
}

// Store atomically reserves all locally owned identities across authorities:
// freeze operation, authority, single-use input authority, generated
// orchestration/run/operation IDs, and the precommitted credential-set ID. The
// Golden fixture UUID is an upstream immutable reference and is checked for a
// collision inside one Record but may be reused by later runs. The plan
// artifact ID is globally reserved as well. ErrStoreOutcomeUnknown requires a
// strongly consistent InspectOperation.
type Store interface {
	Freeze(context.Context, Record) (Record, error)
	InspectOperation(context.Context, uuid.UUID) (Record, error)
	ResolveAuthority(context.Context, uuid.UUID) (Record, error)
}
