// Package qualificationreceiptv3 defines the closed, signable qualification
// receipt that follows an independently verified, immutable evidence snapshot.
//
// This package is deliberately a pure domain and verification boundary. It
// does not resolve plan authorities, execute qualification, call signers, seal
// storage, or authorize promotion. A production caller must resolve every
// Expected Receipt from trusted server storage before calling Verifier.Verify.
package qualificationreceiptv3

import (
	"context"
	"crypto/ed25519"
	"errors"

	"github.com/worksflow/builder/backend/internal/qualificationevidence"
)

const (
	ReceiptSchemaV3                 = "worksflow-qualification-receipt/v3"
	ReceiptPredicateTypeV3          = "https://worksflow.dev/attestations/qualification-receipt/v3"
	InTotoStatementV1               = "https://in-toto.io/Statement/v1"
	InTotoPayloadType               = "application/vnd.in-toto+json"
	PlanAuthoritySchemaV1           = "worksflow-qualification-plan-authority/v1"
	PlanTrustSchemaV1               = "worksflow-qualification-plan-trust/v1"
	PlanTargetSchemaV1              = "worksflow-qualification-plan-target/v1"
	SourceTreeDigestSchemaV1        = "worksflow-source-content-tree/v1"
	EvidenceClosureSchemaV3         = "worksflow-qualification-evidence-closure/v3"
	ArtifactIndexSchemaV3           = "worksflow-qualification-artifact-index/v3"
	PreReceiptSnapshotSchemaV3      = "worksflow-qualification-pre-receipt-snapshot/v3"
	SnapshotVerificationSchemaV3    = "worksflow-qualification-snapshot-verification/v3"
	ReceiptTrustPolicySchemaV3      = "worksflow-qualification-receipt-trust-policy/v3"
	GoldenFaultOperationSetDigestV1 = "sha256:50add6d13b4b28587f5ceab1385d85e457cc35489a031ac9d2f3ff217bd1fa9d"

	PlanArtifactPrefix      = "qualification-plan-"
	ExternalStageGate       = "external-qualification"
	ImmutableSnapshotMode   = "immutable-filesystem"
	AuthorityStageCommitted = "committed"
	VerificationPassed      = "verified"
	DecisionQualified       = "qualified"
	SignerRoleRunner        = "qualification-runner"
	SignerRoleApprover      = "release-approver"

	MaximumArtifacts = qualificationevidence.MaximumArtifacts
	MaximumMembers   = qualificationevidence.MaximumMembers
)

var (
	ErrInvalid      = errors.New("qualification receipt v3 is invalid")
	ErrVerification = errors.New("qualification receipt v3 verification failed")
)

// PlanAuthorityBinding is the complete canonical authority envelope plus its
// hash. AuthorityHash is recomputed from these fields; it is never trusted as
// a free-standing label. PlanDigest and ProjectionHash deliberately coexist so
// domain confusion between the manifest projection and evidence Plan is
// detectable.
type PlanAuthorityBinding struct {
	ArtifactID          string `json:"artifactId"`
	AuthorityHash       string `json:"authorityHash"`
	AuthorityID         string `json:"authorityId"`
	EvidencePlanHash    string `json:"evidencePlanHash"`
	FreezeOperationID   string `json:"freezeOperationId"`
	InputAuthorityID    string `json:"inputAuthorityId"`
	InputHash           string `json:"inputHash"`
	PlanDigest          string `json:"planDigest"`
	ProjectionHash      string `json:"projectionHash"`
	TargetHash          string `json:"targetHash"`
	TrustBindingsDigest string `json:"trustBindingsDigest"`
	TrustHash           string `json:"trustHash"`
}

// PromotionTarget is retained in full as well as through TargetHash so a
// promotion transaction can compare the exact target, not merely a digest
// copied from the signed payload.
type PromotionTarget struct {
	NodeKey        string                  `json:"nodeKey"`
	ProjectID      string                  `json:"projectId"`
	StageGate      string                  `json:"stageGate"`
	Subject        string                  `json:"subject"`
	TargetRevision PromotionTargetRevision `json:"targetRevision"`
	WorkflowRunID  string                  `json:"workflowRunId"`
}

type PromotionTargetRevision struct {
	ContentHash string `json:"contentHash"`
	ID          string `json:"id"`
}

type TargetBinding struct {
	PromotionTarget PromotionTarget `json:"promotionTarget"`
	SchemaVersion   string          `json:"schemaVersion"`
}

// TrustBindings is intentionally the same closed projection hashed by the
// evidence orchestrator. The receipt carries the values and the frozen Plan
// Authority carries their digest; verification requires both to agree.
type TrustBindings struct {
	CaptureAuthorityID    string `json:"captureAuthorityId"`
	CredentialAuthorityID string `json:"credentialAuthorityId"`
	EncryptionAuthorityID string `json:"encryptionAuthorityId"`
	IndexerAuthorityID    string `json:"indexerAuthorityId"`
	KMSAuthorityID        string `json:"kmsAuthorityId"`
	ReceiptAuthorityID    string `json:"receiptAuthorityId"`
	SealerAuthorityID     string `json:"sealerAuthorityId"`
	VerifierAuthorityID   string `json:"verifierAuthorityId"`
}

type TrustBinding struct {
	SchemaVersion     string        `json:"schemaVersion"`
	TrustBindings     TrustBindings `json:"trustBindings"`
	TrustPolicyDigest string        `json:"trustPolicyDigest"`
}

type SourceBinding struct {
	Commit           string `json:"commit"`
	Dirty            bool   `json:"dirty"`
	TreeDigest       string `json:"treeDigest"`
	TreeDigestSchema string `json:"treeDigestSchema"`
}

type TemplateReleaseBinding struct {
	ApprovalReceiptDigest string `json:"approvalReceiptDigest"`
	ContentHash           string `json:"contentHash"`
	ID                    string `json:"id"`
}

type ImmutableContentBinding struct {
	ContentHash string `json:"contentHash"`
	ID          string `json:"id"`
}

type ArtifactRevisionBinding struct {
	ArtifactID  string `json:"artifactId"`
	ContentHash string `json:"contentHash"`
	RevisionID  string `json:"revisionId"`
}

type BuildBinding struct {
	Contract ImmutableContentBinding `json:"contract"`
	Manifest ImmutableContentBinding `json:"manifest"`
}

type GoldenRuntimeBinding struct {
	AuthorityDocumentArtifactID string `json:"authorityDocumentArtifactId"`
	AuthorityDocumentDigest     string `json:"authorityDocumentDigest"`
	FaultOperationSetDigest     string `json:"faultOperationSetDigest"`
	FixtureDocumentArtifactID   string `json:"fixtureDocumentArtifactId"`
	FixtureDocumentDigest       string `json:"fixtureDocumentDigest"`
	FixtureID                   string `json:"fixtureId"`
}

type SignedArtifactBinding struct {
	ArtifactID      string `json:"artifactId"`
	ContentDigest   string `json:"contentDigest"`
	PayloadDigest   string `json:"payloadDigest"`
	SignerSetDigest string `json:"signerSetDigest"`
}

// CredentialSetBinding proves that the exact issued set was revoked before
// the pre-Receipt snapshot was sealed. It carries commitments only, never a
// secret credential or retrievable handle.
type CredentialSetBinding struct {
	Audience             string                `json:"audience"`
	ExpiresAt            string                `json:"expiresAt"`
	Issuance             SignedArtifactBinding `json:"issuance"`
	IssuedAt             string                `json:"issuedAt"`
	Issuer               string                `json:"issuer"`
	MemberBindingsDigest string                `json:"memberBindingsDigest"`
	MemberCount          int                   `json:"memberCount"`
	Revocation           SignedArtifactBinding `json:"revocation"`
	RevokedAt            string                `json:"revokedAt"`
	SetHandleHash        string                `json:"setHandleHash"`
	SetID                string                `json:"setId"`
}

// EvidenceClosureBinding is the exact pre-index evidence set. ArtifactIDs are
// the complete sorted set and RestrictedArtifactIDs are its complete sorted
// restricted subset; counts are repeated and cross-checked to prevent a
// consumer from trusting a count without the corresponding identities.
type EvidenceClosureBinding struct {
	ArtifactIDs              []string `json:"artifactIds"`
	ArtifactSetDigest        string   `json:"artifactSetDigest"`
	CaptureDigest            string   `json:"captureDigest"`
	ClosureDigest            string   `json:"closureDigest"`
	EncryptionManifestDigest string   `json:"encryptionManifestDigest"`
	KMSAttestationDigest     string   `json:"kmsAttestationDigest"`
	OrchestrationID          string   `json:"orchestrationId"`
	RestrictedArtifactIDs    []string `json:"restrictedArtifactIds"`
	ResultDigest             string   `json:"resultDigest"`
	RunID                    string   `json:"runId"`
	SchemaVersion            string   `json:"schemaVersion"`
}

type ArtifactIndexBinding struct {
	ArtifactCount           int                         `json:"artifactCount"`
	ArtifactIDs             []string                    `json:"artifactIds"`
	Artifacts               []ArtifactDescriptorBinding `json:"artifacts"`
	ArtifactSetDigest       string                      `json:"artifactSetDigest"`
	AuthorityID             string                      `json:"authorityId"`
	ContentDigest           string                      `json:"contentDigest"`
	CommittedAt             string                      `json:"committedAt"`
	EvidenceClosureDigest   string                      `json:"evidenceClosureDigest"`
	IndexID                 string                      `json:"indexId"`
	OperationID             string                      `json:"operationId"`
	RequestDigest           string                      `json:"requestDigest"`
	RestrictedArtifactCount int                         `json:"restrictedArtifactCount"`
	RestrictedArtifactIDs   []string                    `json:"restrictedArtifactIds"`
	SchemaVersion           string                      `json:"schemaVersion"`
	Stage                   string                      `json:"stage"`
}

type ArtifactDescriptorBinding struct {
	ContentDigest string `json:"contentDigest"`
	ID            string `json:"id"`
}

// PreReceiptSnapshotBinding cannot carry a Receipt ID, digest, payload, or
// envelope. It is sealed and independently verified before the Receipt
// payload exists, which makes SnapshotDigest acyclic.
type PreReceiptSnapshotBinding struct {
	ArtifactIndexDigest   string `json:"artifactIndexDigest"`
	AuthorityID           string `json:"authorityId"`
	EvidenceClosureDigest string `json:"evidenceClosureDigest"`
	Mode                  string `json:"mode"`
	OperationID           string `json:"operationId"`
	RequestDigest         string `json:"requestDigest"`
	SchemaVersion         string `json:"schemaVersion"`
	SealedAt              string `json:"sealedAt"`
	SnapshotDigest        string `json:"snapshotDigest"`
	SnapshotID            string `json:"snapshotId"`
	Stage                 string `json:"stage"`
}

type SnapshotVerificationBinding struct {
	ArtifactIndexDigest   string `json:"artifactIndexDigest"`
	AuthorityID           string `json:"authorityId"`
	EvidenceClosureDigest string `json:"evidenceClosureDigest"`
	Result                string `json:"result"`
	SchemaVersion         string `json:"schemaVersion"`
	SnapshotDigest        string `json:"snapshotDigest"`
	SnapshotID            string `json:"snapshotId"`
	VerifiedAt            string `json:"verifiedAt"`
}

type SignerIdentityBinding struct {
	Identity string `json:"identity"`
	KeyID    string `json:"keyId"`
	Role     string `json:"role"`
}

type ReceiptSignerBinding struct {
	Approver SignerIdentityBinding `json:"approver"`
	Runner   SignerIdentityBinding `json:"runner"`
}

// Receipt is the v3 in-toto predicate. EvidencePlan is embedded as typed
// canonical JSON so EvidencePlanHash can be independently recomputed. The
// snapshot and verification bind only pre-Receipt state.
type Receipt struct {
	ArtifactIndex         ArtifactIndexBinding       `json:"artifactIndex"`
	Build                 BuildBinding               `json:"build"`
	CompletedAt           string                     `json:"completedAt"`
	CredentialSet         CredentialSetBinding       `json:"credentialSet"`
	Decision              string                     `json:"decision"`
	Evidence              EvidenceClosureBinding     `json:"evidence"`
	EvidencePlan          qualificationevidence.Plan `json:"evidencePlan"`
	GoldenRuntime         GoldenRuntimeBinding       `json:"goldenRuntime"`
	IssuedAt              string                     `json:"issuedAt"`
	OperationID           string                     `json:"operationId"`
	PlanAuthority         PlanAuthorityBinding       `json:"planAuthority"`
	QualificationManifest ArtifactRevisionBinding    `json:"qualificationManifest"`
	// QualificationStartedAt is the qualification run start. It is not proof
	// that a signer call was durably recorded before invocation; that proof must
	// come from a separate append-only request record using trusted store time.
	QualificationStartedAt string                      `json:"qualificationStartedAt"`
	ReceiptID              string                      `json:"receiptId"`
	SchemaVersion          string                      `json:"schemaVersion"`
	Signers                ReceiptSignerBinding        `json:"signers"`
	Snapshot               PreReceiptSnapshotBinding   `json:"snapshot"`
	SnapshotVerification   SnapshotVerificationBinding `json:"snapshotVerification"`
	Source                 SourceBinding               `json:"source"`
	Target                 TargetBinding               `json:"target"`
	TemplateRelease        TemplateReleaseBinding      `json:"templateRelease"`
	Trust                  TrustBinding                `json:"trust"`
}

type InTotoSubject struct {
	Digest map[string]string `json:"digest"`
	Name   string            `json:"name"`
}

type InTotoStatement struct {
	Type          string          `json:"_type"`
	Predicate     Receipt         `json:"predicate"`
	PredicateType string          `json:"predicateType"`
	Subject       []InTotoSubject `json:"subject"`
}

type CompiledPayload struct {
	Payload       []byte
	PayloadDigest string
	Receipt       Receipt
	SubjectDigest string
	SubjectName   string
}

// SignerTrust is keyful server-owned policy. Only Ed25519 is admitted in v3 so
// deterministic key separation can be checked without an algorithm label
// supplied by the envelope.
type SignerTrust struct {
	Identity  string
	KeyID     string
	PublicKey ed25519.PublicKey
}

type TrustPolicy struct {
	Digest        string
	SchemaVersion string
	Approver      SignerTrust
	Runner        SignerTrust
}

// ExpectedResolution is returned only by a trusted server-side resolver. The
// exact canonical payload and digest must come from immutable authority state,
// never from the submitted DSSE envelope.
type ExpectedResolution struct {
	AuthorityID   string
	Payload       []byte
	PayloadDigest string
	ReceiptID     string
}

// ExpectedResolver is the authorization provenance boundary for Verify. A
// production implementation resolves opaque IDs through trusted immutable
// storage. This package intentionally supplies no wire-derived or in-memory
// production resolver.
type ExpectedResolver interface {
	ResolveExpected(context.Context, string, string) (ExpectedResolution, error)
}

type VerifiedSigner struct {
	Identity string
	KeyID    string
	Role     string
}

type VerifiedReceipt struct {
	Approver          VerifiedSigner
	CanonicalEnvelope []byte
	EnvelopeDigest    string
	Payload           []byte
	PayloadDigest     string
	Receipt           Receipt
	Runner            VerifiedSigner
	SubjectDigest     string
	SubjectName       string
}
