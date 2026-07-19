// Package qualificationreceipt verifies the immutable, signed evidence used
// by the external qualification promotion gate.
package qualificationreceipt

import (
	"time"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const (
	ArtifactIndexSchemaV1           = "worksflow-qualification-artifact-index/v1"
	ReceiptSchemaV2                 = "worksflow-qualification-receipt/v2"
	TrustPolicySchemaV2             = "worksflow-qualification-trust-policy/v2"
	CredentialSetIssuanceSchemaV1   = "worksflow-credential-set-issuance/v1"
	CredentialSetRevocationSchemaV1 = "worksflow-credential-set-revocation/v1"
	CredentialSetMembersSchemaV1    = "worksflow-credential-set-member-bindings/v1"
	EncryptionAttestationSchemaV1   = "worksflow-evidence-encryption-attestation/v1"
	GoldenFaultLedgerSchemaV1       = "worksflow-golden-fault-ledger-attestation/v1"
	QualificationExpectationV1      = "worksflow-qualification-expectation/v1"
	PlaywrightResultSchemaV1        = "worksflow-playwright-qualification-result/v1"
	WriterDrainProofSchemaV1        = "worksflow-writer-drain-proof/v1"

	InTotoPayloadType                      = "application/vnd.in-toto+json"
	QualificationPredicateTypeV2           = "https://worksflow.dev/attestations/qualification-receipt/v2"
	CredentialSetIssuancePredicateTypeV1   = "https://worksflow.dev/attestations/credential-set-issuance/v1"
	CredentialSetRevocationPredicateTypeV1 = "https://worksflow.dev/attestations/credential-set-revocation/v1"
	EncryptionPredicateTypeV1              = "https://worksflow.dev/attestations/evidence-encryption/v1"
	GoldenFaultLedgerPredicateTypeV1       = "https://worksflow.dev/attestations/golden-fault-ledger/v1"

	CompilerVersionV7 = "worksflow-constraint-compiler/v7"
	CompilerVersionV6 = "worksflow-constraint-compiler/v6"

	ArtifactTypeEvidence                = "evidence"
	ArtifactTypePlaywrightResults       = "playwright-results"
	ArtifactTypeCredentialSetIssuance   = "credential-set-issuance"
	ArtifactTypeCredentialSetRevocation = "credential-set-revocation"
	ArtifactTypeGoldenAuthorityDocument = "golden-authority-document"
	ArtifactTypeGoldenFixtureDocument   = "golden-fixture-document"
	ArtifactTypeTrace                   = "credential-safe-trace"
	ArtifactTypeVideo                   = "browser-video"
	ArtifactTypeWriterDrain             = "writer-drain-proof"
	ArtifactTypeEncryptionAttestation   = "encryption-attestation"
	ArtifactTypeGoldenFaultAuthority    = "golden-fault-authority"
	ArtifactTypeGoldenFaultReceipt      = "golden-fault-consume-receipt"
	ArtifactTypeGoldenFaultLedger       = "golden-fault-ledger-attestation"
	EncryptionAttestationArtifactID     = "kms-encryption-attestation"
	GoldenFaultLedgerArtifactID         = "golden-fault-ledger-attestation"
	DSSEEnvelopeMediaType               = "application/vnd.dsse.envelope.v1+json"
	CanonicalJSONMediaType              = "application/json"

	ClassificationDistributable       = "distributable"
	ClassificationRestrictedEncrypted = "restricted-encrypted"
	EncryptionSchemeV1                = "AES-256-GCM+KMS/v1"
	ImmutableSnapshotMode             = "immutable-filesystem"

	SignerRoleRunner              = "qualification-runner"
	SignerRoleApprover            = "release-approver"
	SignerRoleFaultOperator       = "fault-operator"
	SignerRoleFaultLedgerAttestor = "fault-ledger-attestor"

	ExternalQualificationScope  = "external-qualification-only"
	ExternalQualificationGate   = "external-qualification"
	SingleUseConsumptionPolicy  = "downstream-append-only-ledger-cas-required"
	CredentialSetMaximumMembers = 64
)

// PromotionTarget is the canonical server-owned identity of the stage gate
// for which a qualification receipt may be consumed. A consumer must compare
// every field with its own transaction target; the CLI never accepts these
// values from an untrusted flag.
type PromotionTarget struct {
	ProjectID      string                  `json:"projectId"`
	WorkflowRunID  string                  `json:"workflowRunId"`
	NodeKey        string                  `json:"nodeKey"`
	TargetRevision PromotionTargetRevision `json:"targetRevision"`
	Subject        string                  `json:"subject"`
	StageGate      string                  `json:"stageGate"`
}

type PromotionTargetRevision struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type ArtifactIndex struct {
	SchemaVersion   string                 `json:"schemaVersion"`
	RunID           string                 `json:"runId"`
	PlanDigest      string                 `json:"planDigest"`
	Source          SourceBinding          `json:"source"`
	TemplateRelease TemplateReleaseBinding `json:"templateRelease"`
	Artifacts       []ArtifactDescriptor   `json:"artifacts"`
}

type SourceBinding struct {
	Commit           string `json:"commit"`
	TreeDigestSchema string `json:"treeDigestSchema"`
	// TreeDigest is worksflow-source-content-tree/v1: an independent SHA-256
	// commitment over canonical path, Git mode, size, and SHA-256(actual bytes).
	// It is deliberately not a digest of Git object IDs.
	TreeDigest string `json:"treeDigest"`
	Dirty      bool   `json:"dirty"`
}

type TemplateReleaseBinding struct {
	ID                    string `json:"id"`
	ContentHash           string `json:"contentHash"`
	ApprovalReceiptDigest string `json:"approvalReceiptDigest"`
}

type ArtifactDescriptor struct {
	ID             string                `json:"id"`
	Path           string                `json:"path"`
	Type           string                `json:"type"`
	MediaType      string                `json:"mediaType"`
	SHA256         string                `json:"sha256"`
	SizeBytes      int64                 `json:"sizeBytes"`
	Classification string                `json:"classification"`
	SuiteIDs       []string              `json:"suiteIds"`
	RequirementIDs []string              `json:"requirementIds"`
	Encryption     *EncryptionDescriptor `json:"encryption,omitempty"`
}

type EncryptionDescriptor struct {
	Scheme             string `json:"scheme"`
	KeyResource        string `json:"keyResource"`
	KeyVersion         string `json:"keyVersion"`
	WrappedKey         string `json:"wrappedKey"`
	Nonce              string `json:"nonce"`
	Tag                string `json:"tag"`
	AdditionalData     string `json:"additionalData"`
	AdditionalDataHash string `json:"additionalDataHash"`
}

type QualificationReceipt struct {
	SchemaVersion                 string                 `json:"schemaVersion"`
	Scope                         string                 `json:"scope"`
	PromotionTarget               PromotionTarget        `json:"promotionTarget"`
	AuthorityNonce                string                 `json:"authorityNonce"`
	AuthorityExpiresAt            string                 `json:"authorityExpiresAt"`
	RunID                         string                 `json:"runId"`
	PlanDigest                    string                 `json:"planDigest"`
	PrePromotionManifestDigest    string                 `json:"prePromotionManifestDigest"`
	TrustPolicyDigest             string                 `json:"trustPolicyDigest"`
	SourcePolicyAttestationDigest string                 `json:"sourcePolicyAttestationDigest"`
	Source                        SourceBinding          `json:"source"`
	TemplateRelease               TemplateReleaseBinding `json:"templateRelease"`
	GoldenRuntime                 GoldenRuntimeBinding   `json:"goldenRuntime"`
	Constructor                   ConstructorBinding     `json:"constructor"`
	Suites                        []SuiteResult          `json:"suites"`
	Totals                        QualificationTotals    `json:"totals"`
	CredentialSet                 CredentialSetBinding   `json:"credentialSet"`
	ArtifactIndex                 ArtifactIndexBinding   `json:"artifactIndex"`
	Decision                      string                 `json:"decision"`
	StartedAt                     string                 `json:"startedAt"`
	CompletedAt                   string                 `json:"completedAt"`
	IssuedAt                      string                 `json:"issuedAt"`
}

type ConstructorBinding struct {
	CompilerVersion   string             `json:"compilerVersion"`
	BuildContractHash string             `json:"buildContractHash"`
	WriterDrain       WriterDrainBinding `json:"writerDrain"`
}

type WriterDrainBinding struct {
	FromVersion        string `json:"fromVersion"`
	ToVersion          string `json:"toVersion"`
	Status             string `json:"status"`
	ActiveWriters      int    `json:"activeWriters"`
	InFlightMutations  int    `json:"inFlightMutations"`
	CompletedAt        string `json:"completedAt"`
	EvidenceArtifactID string `json:"evidenceArtifactId"`
}

type SuiteResult struct {
	ID                  string   `json:"id"`
	Result              string   `json:"result"`
	RequirementIDs      []string `json:"requirementIds"`
	TestInventoryDigest string   `json:"testInventoryDigest"`
	ArtifactIDs         []string `json:"artifactIds"`
}

type QualificationTotals struct {
	Discovered int `json:"discovered"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
	Flaky      int `json:"flaky"`
	Retried    int `json:"retried"`
	Mocked     int `json:"mocked"`
}

type GoldenRuntimeBinding struct {
	AuthorityDocumentArtifactID string `json:"authorityDocumentArtifactId"`
	AuthorityDocumentDigest     string `json:"authorityDocumentDigest"`
	FaultOperationSetDigest     string `json:"faultOperationSetDigest"`
	FixtureDocumentArtifactID   string `json:"fixtureDocumentArtifactId"`
	FixtureDocumentDigest       string `json:"fixtureDocumentDigest"`
	FixtureID                   string `json:"fixtureId"`
}

type CredentialSetAuthorityBinding struct {
	Issuer               string `json:"issuer"`
	Audience             string `json:"audience"`
	SetHandleHash        string `json:"setHandleHash"`
	MemberBindingsDigest string `json:"memberBindingsDigest"`
	MemberCount          int    `json:"memberCount"`
}

type CredentialSetArtifactBinding struct {
	ArtifactID    string `json:"artifactId"`
	PayloadDigest string `json:"payloadDigest"`
}

type CredentialSetBinding struct {
	Issuer               string                       `json:"issuer"`
	Audience             string                       `json:"audience"`
	SetHandleHash        string                       `json:"setHandleHash"`
	MemberBindingsDigest string                       `json:"memberBindingsDigest"`
	MemberCount          int                          `json:"memberCount"`
	IssuedAt             string                       `json:"issuedAt"`
	ExpiresAt            string                       `json:"expiresAt"`
	RevokedAt            string                       `json:"revokedAt"`
	Issuance             CredentialSetArtifactBinding `json:"issuance"`
	Revocation           CredentialSetArtifactBinding `json:"revocation"`
}

type ArtifactIndexBinding struct {
	Digest                   string `json:"digest"`
	Count                    int    `json:"count"`
	RestrictedEncryptedCount int    `json:"restrictedEncryptedCount"`
}

type CredentialSetMember struct {
	Slot                 string `json:"slot"`
	ActorID              string `json:"actorId"`
	Kind                 string `json:"kind"`
	CredentialHandleHash string `json:"credentialHandleHash"`
}

type CredentialSetIssuance struct {
	SchemaVersion        string                `json:"schemaVersion"`
	RunID                string                `json:"runId"`
	FixtureID            string                `json:"fixtureId"`
	Issuer               string                `json:"issuer"`
	Audience             string                `json:"audience"`
	SetHandleHash        string                `json:"setHandleHash"`
	MemberBindingsDigest string                `json:"memberBindingsDigest"`
	MemberCount          int                   `json:"memberCount"`
	Members              []CredentialSetMember `json:"members"`
	Status               string                `json:"status"`
	IssuedAt             string                `json:"issuedAt"`
	ExpiresAt            string                `json:"expiresAt"`
}

type CredentialSetRevocation struct {
	SchemaVersion        string                `json:"schemaVersion"`
	RunID                string                `json:"runId"`
	FixtureID            string                `json:"fixtureId"`
	Issuer               string                `json:"issuer"`
	Audience             string                `json:"audience"`
	SetHandleHash        string                `json:"setHandleHash"`
	MemberBindingsDigest string                `json:"memberBindingsDigest"`
	MemberCount          int                   `json:"memberCount"`
	Members              []CredentialSetMember `json:"members"`
	Status               string                `json:"status"`
	IssuedAt             string                `json:"issuedAt"`
	ExpiresAt            string                `json:"expiresAt"`
	RevokedAt            string                `json:"revokedAt"`
}

type EncryptionAttestation struct {
	SchemaVersion   string                       `json:"schemaVersion"`
	RunID           string                       `json:"runId"`
	PlanDigest      string                       `json:"planDigest"`
	TemplateRelease TemplateReleaseBinding       `json:"templateRelease"`
	ManifestDigest  string                       `json:"manifestDigest"`
	Artifacts       []EncryptionAttestedArtifact `json:"artifacts"`
	IssuedAt        string                       `json:"issuedAt"`
}

type EncryptionAttestedArtifact struct {
	ArtifactID                 string `json:"artifactId"`
	Path                       string `json:"path"`
	CiphertextDigest           string `json:"ciphertextDigest"`
	SizeBytes                  int64  `json:"sizeBytes"`
	KeyResource                string `json:"keyResource"`
	KeyVersion                 string `json:"keyVersion"`
	WrappedKeyDigest           string `json:"wrappedKeyDigest"`
	AdditionalDataHash         string `json:"additionalDataHash"`
	EncryptionDescriptorDigest string `json:"encryptionDescriptorDigest"`
	EncryptedAt                string `json:"encryptedAt"`
	PlaintextDisposition       string `json:"plaintextDisposition"`
	PlaintextDispositionAt     string `json:"plaintextDispositionAt"`
	Status                     string `json:"status"`
}

type GoldenFaultLedgerAttestation struct {
	Entries       []GoldenFaultLedgerEntry `json:"entries"`
	FixtureID     string                   `json:"fixtureId"`
	IssuedAt      string                   `json:"issuedAt"`
	RunID         string                   `json:"runId"`
	SchemaVersion string                   `json:"schemaVersion"`
	Status        string                   `json:"status"`
}

type GoldenFaultLedgerEntry struct {
	AuthorityDigest   string `json:"authorityDigest"`
	AuthorityID       string `json:"authorityId"`
	CompletedAt       string `json:"completedAt"`
	EnvelopeDigest    string `json:"envelopeDigest"`
	OperationKind     string `json:"operationKind"`
	Outcome           string `json:"outcome"`
	PayloadDigest     string `json:"payloadDigest"`
	ReceiptArtifactID string `json:"receiptArtifactId"`
	ReceiptDigest     string `json:"receiptDigest"`
	ReservationDigest string `json:"reservationDigest"`
	ReservedAt        string `json:"reservedAt"`
	ResultDigest      string `json:"resultDigest"`
	ResultID          string `json:"resultId"`
	State             string `json:"state"`
}

type PlaywrightQualificationResult struct {
	SchemaVersion       string                 `json:"schemaVersion"`
	RunID               string                 `json:"runId"`
	TestInventoryDigest string                 `json:"testInventoryDigest"`
	Config              PlaywrightResultConfig `json:"config"`
	Tests               []PlaywrightTestResult `json:"tests"`
	Totals              QualificationTotals    `json:"totals"`
}

type PlaywrightResultConfig struct {
	ForbidOnly bool `json:"forbidOnly"`
	Retries    int  `json:"retries"`
	Workers    int  `json:"workers"`
}

type PlaywrightTestResult struct {
	CaseID               string   `json:"caseId"`
	SuiteID              string   `json:"suiteId"`
	RequirementIDs       []string `json:"requirementIds"`
	ContractCriterionIDs []string `json:"contractCriterionIds"`
	Status               string   `json:"status"`
	Retry                int      `json:"retry"`
	Flaky                bool     `json:"flaky"`
	Mocked               bool     `json:"mocked"`
}

type WriterDrainProof struct {
	SchemaVersion     string                 `json:"schemaVersion"`
	PlanDigest        string                 `json:"planDigest"`
	TemplateRelease   TemplateReleaseBinding `json:"templateRelease"`
	FromVersion       string                 `json:"fromVersion"`
	ToVersion         string                 `json:"toVersion"`
	Status            string                 `json:"status"`
	ActiveWriters     int                    `json:"activeWriters"`
	InFlightMutations int                    `json:"inFlightMutations"`
	CompletedAt       string                 `json:"completedAt"`
}

type ExpectedSuite struct {
	ID                string
	RequirementIDs    []string
	RequiredArtifacts []string
}

type ExpectedTestCase struct {
	CaseID               string
	SuiteID              string
	RequirementIDs       []string
	ContractCriterionIDs []string
	File                 string
	Title                string
	Mode                 string
}

type ExpectedPromotion struct {
	PromotionTarget               PromotionTarget
	AuthorityNonce                string
	AuthorityExpiresAt            string
	AuthorityIssuedAt             string
	PromotionAuthorityDigest      string
	RunID                         string
	PlanDigest                    string
	PrePromotionManifestDigest    string
	Source                        SourceBinding
	TemplateRelease               TemplateReleaseBinding
	GoldenRuntime                 GoldenRuntimeBinding
	BuildContractHash             string
	WriterDrainEvidenceArtifactID string
	CredentialSet                 CredentialSetAuthorityBinding
	SourcePolicyAttestationDigest string
	ArtifactRoot                  string
	EvidenceSnapshotRoot          string
	ArtifactIndexDigest           string
	ReceiptBundleDigest           string
	TrustedReceiptIssuedAt        string
	ArtifactSnapshotID            string
	ArtifactSnapshotMode          string
	Suites                        []ExpectedSuite
	TestInventoryDigest           string
	TestCases                     []ExpectedTestCase
	VerifiedAt                    time.Time
}

type SignerTrust struct {
	Algorithm templateauthority.SignatureAlgorithm
	PublicKey any
	Identity  string
	Role      string
	NotBefore time.Time
	NotAfter  time.Time
	RevokedAt *time.Time
}

type CredentialIssuerTrust struct {
	Issuer            string
	Keys              map[string]templateauthority.TrustedSigner
	KeyValidity       map[string]AuthorityKeyValidity
	MinimumSignatures int
	AllowedIdentities []string
}

type EncryptionAuthorityTrust struct {
	Keys              map[string]templateauthority.TrustedSigner
	KeyValidity       map[string]AuthorityKeyValidity
	MinimumSignatures int
	AllowedIdentities []string
}

type FaultAuthorityTrust struct {
	Keys              map[string]templateauthority.TrustedSigner
	KeyValidity       map[string]AuthorityKeyValidity
	MinimumSignatures int
	AllowedIdentities []string
}

type FaultLedgerAttestorTrust struct {
	Keys              map[string]templateauthority.TrustedSigner
	KeyValidity       map[string]AuthorityKeyValidity
	MinimumSignatures int
	AllowedIdentities []string
}

type AuthorityKeyValidity struct {
	NotBefore time.Time
	NotAfter  time.Time
	RevokedAt *time.Time
}

type EncryptionRecipient struct {
	KeyResource string
	KeyVersion  string
}

type TrustPolicy struct {
	Digest               string
	Signers              map[string]SignerTrust
	MinimumSignatures    int
	MaxReceiptAge        time.Duration
	MaxFutureSkew        time.Duration
	CredentialIssuers    map[string]CredentialIssuerTrust
	EncryptionRecipients []EncryptionRecipient
	EncryptionAuthority  EncryptionAuthorityTrust
	FaultAuthority       FaultAuthorityTrust
	FaultLedgerAttestor  FaultLedgerAttestorTrust
}

type VerifiedPromotion struct {
	Scope                                string                       `json:"scope"`
	PromotionTarget                      PromotionTarget              `json:"promotionTarget"`
	AuthorityNonce                       string                       `json:"authorityNonce"`
	AuthorityExpiresAt                   string                       `json:"authorityExpiresAt"`
	PromotionAuthorityDigest             string                       `json:"promotionAuthorityDigest"`
	GoldenRuntime                        GoldenRuntimeBinding         `json:"goldenRuntime"`
	CredentialSet                        VerifiedCredentialSetBinding `json:"credentialSet"`
	SingleUseConsumption                 string                       `json:"singleUseConsumption"`
	RunID                                string                       `json:"runId"`
	PlanDigest                           string                       `json:"planDigest"`
	ArtifactIndexDigest                  string                       `json:"artifactIndexDigest"`
	ReceiptPayloadDigest                 string                       `json:"receiptPayloadDigest"`
	ReceiptBundleDigest                  string                       `json:"receiptBundleDigest"`
	SignerIdentities                     []string                     `json:"signerIdentities"`
	CredentialIssuanceSignerIdentities   []string                     `json:"credentialIssuanceSignerIdentities"`
	CredentialRevocationSignerIdentities []string                     `json:"credentialRevocationSignerIdentities"`
	EncryptionSignerIdentities           []string                     `json:"encryptionSignerIdentities"`
	FaultAuthoritySignerIdentities       []string                     `json:"faultAuthoritySignerIdentities"`
	FaultLedgerAttestationDigest         string                       `json:"faultLedgerAttestationDigest"`
	FaultLedgerAttestorSignerIdentities  []string                     `json:"faultLedgerAttestorSignerIdentities"`
	IssuedAt                             string                       `json:"issuedAt"`
	Decision                             string                       `json:"decision"`
}

type VerifiedCredentialSetBinding struct {
	Issuer                  string `json:"issuer"`
	Audience                string `json:"audience"`
	SetHandleHash           string `json:"setHandleHash"`
	MemberBindingsDigest    string `json:"memberBindingsDigest"`
	MemberCount             int    `json:"memberCount"`
	IssuanceArtifactID      string `json:"issuanceArtifactId"`
	IssuancePayloadDigest   string `json:"issuancePayloadDigest"`
	RevocationArtifactID    string `json:"revocationArtifactId"`
	RevocationPayloadDigest string `json:"revocationPayloadDigest"`
}
