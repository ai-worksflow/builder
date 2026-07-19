package modelgovernance

import (
	"crypto/ed25519"
	"errors"
	"time"
)

const (
	GovernanceEnvelopePayloadTypeConformance    = "application/vnd.worksflow.model-conformance-result+json;version=1"
	GovernanceEnvelopePayloadTypeShadow         = "application/vnd.worksflow.model-shadow-comparison+json;version=1"
	GovernanceEnvelopePayloadTypeApproval       = "application/vnd.worksflow.model-profile-approval+json;version=1"
	GovernanceEnvelopePayloadTypeActivation     = "application/vnd.worksflow.model-activation-decision+json;version=1"
	GovernanceEnvelopePayloadTypeReceipt        = "application/vnd.worksflow.model-governance-receipt+json;version=1"
	GovernanceEnvelopePayloadTypeGenesis        = "application/vnd.worksflow.model-governance-genesis-decision+json;version=1"
	GovernanceEnvelopePayloadTypeGenesisReceipt = "application/vnd.worksflow.model-governance-genesis-receipt+json;version=1"

	ConformanceArtifactSchemaVersion      = "worksflow-model-conformance-result/v1"
	ShadowArtifactSchemaVersion           = "worksflow-model-shadow-comparison/v1"
	ApprovalArtifactSchemaVersion         = "worksflow-model-profile-approval/v1"
	ActivationArtifactSchemaVersion       = "worksflow-model-activation-decision/v1"
	GovernanceReceiptSchemaVersion        = "worksflow-model-governance-receipt/v1"
	GovernanceGenesisSchemaVersion        = "worksflow-model-governance-genesis-decision/v1"
	GovernanceGenesisReceiptSchemaVersion = "worksflow-model-governance-genesis-receipt/v1"
	ProviderRouteAuthoritySchemaV1        = "worksflow-provider-route-authority/v1"
	GovernanceTrustPolicySchemaV1         = "worksflow-model-governance-trust-policy/v1"
	GovernanceRevocationSchemaV1          = "worksflow-model-governance-revocation-authority/v1"

	RoleConformanceVerifier = "conformance-verifier"
	RoleShadowVerifier      = "shadow-verifier"
	RoleProfileApprover     = "profile-approval-signer"
	RoleActivationApprover  = "activation-approver"
	RoleReceiptIssuer       = "governance-receipt-issuer"
	RoleGenesisApprover     = "genesis-approver"

	ConformanceResultPassed         = "passed"
	ShadowResultPassed              = "passed"
	ApprovalDecisionApprove         = "approve"
	ActivationDecisionApply         = "activate"
	GenesisDecisionBootstrap        = "bootstrap"
	GovernanceRevocationAuthorityID = "model-governance-revocations"

	MaximumGovernanceArtifactLifetime  = 30 * 24 * time.Hour
	MaximumRouteAuthorityLifetime      = 90 * 24 * time.Hour
	MaximumGovernanceClockSkew         = 30 * time.Second
	MaximumDisableStateLifetime        = 5 * time.Minute
	MaximumRevocationAuthorityLifetime = 5 * time.Minute
)

var (
	ErrGovernanceInvalid        = errors.New("model governance authority is invalid")
	ErrGovernanceUntrusted      = errors.New("model governance signer is untrusted")
	ErrActivationConflict       = errors.New("model activation compare-and-swap conflict")
	ErrActivationNotFound       = errors.New("model activation record not found")
	ErrActivationOutcomeUnknown = errors.New("model activation outcome is unknown; inspect the same operation")
	ErrProfileDisabled          = errors.New("active model profile is disabled")
	ErrRuntimeAuthority         = errors.New("active model runtime authority cannot be revalidated")
)

// GovernanceEnvelope is a one-signature DSSE envelope. Each payload type has
// one closed trust role; a payload cannot select its own role.
type GovernanceEnvelope struct {
	Payload     string                `json:"payload"`
	PayloadType string                `json:"payloadType"`
	Signatures  []GovernanceSignature `json:"signatures"`
}

type GovernanceSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type GovernanceArtifactRef struct {
	ArtifactID     string `json:"artifactId"`
	EnvelopeDigest string `json:"envelopeDigest"`
	PayloadDigest  string `json:"payloadDigest"`
}

type GovernanceCorpusBinding struct {
	ContentHash string `json:"contentHash"`
	ID          string `json:"id"`
}

type GovernanceProviderRouteBinding struct {
	AuthorityHash string `json:"authorityHash"`
	RouteID       string `json:"routeId"`
}

type GovernanceSourceBinding struct {
	Commit           string `json:"commit"`
	Dirty            bool   `json:"dirty"`
	TreeDigest       string `json:"treeDigest"`
	TreeDigestSchema string `json:"treeDigestSchema"`
}

// GovernanceSubjectBinding is repeated byte-for-byte in every signed decision
// so no stage can silently substitute a different model, corpus, evaluator,
// route authority, runner or source tree.
type GovernanceSubjectBinding struct {
	Corpus              GovernanceCorpusBinding        `json:"corpus"`
	HarnessHash         string                         `json:"harnessHash"`
	Profile             CorpusProfileBinding           `json:"profile"`
	ProviderRoute       GovernanceProviderRouteBinding `json:"providerRoute"`
	Runner              RunnerBinding                  `json:"runner"`
	Source              GovernanceSourceBinding        `json:"source"`
	ThresholdPolicyHash string                         `json:"thresholdPolicyHash"`
	TrustPolicyHash     string                         `json:"trustPolicyHash"`
	VerifierHash        string                         `json:"verifierHash"`
}

type ConformanceArtifact struct {
	ArtifactID    string                   `json:"artifactId"`
	CompletedAt   string                   `json:"completedAt"`
	ExpiresAt     string                   `json:"expiresAt"`
	IssuedAt      string                   `json:"issuedAt"`
	Result        string                   `json:"result"`
	ResultHash    string                   `json:"resultHash"`
	SchemaVersion string                   `json:"schemaVersion"`
	StartedAt     string                   `json:"startedAt"`
	Subject       GovernanceSubjectBinding `json:"subject"`
}

type BaselineBinding struct {
	ActivationFence string               `json:"activationFence"`
	Generation      uint64               `json:"generation"`
	MetricsHash     string               `json:"metricsHash"`
	Profile         CorpusProfileBinding `json:"profile"`
	ReceiptDigest   string               `json:"receiptDigest"`
}

type ShadowArtifact struct {
	ArtifactID     string                   `json:"artifactId"`
	Baseline       BaselineBinding          `json:"baseline"`
	ComparisonHash string                   `json:"comparisonHash"`
	CompletedAt    string                   `json:"completedAt"`
	ExpiresAt      string                   `json:"expiresAt"`
	IssuedAt       string                   `json:"issuedAt"`
	Result         string                   `json:"result"`
	SchemaVersion  string                   `json:"schemaVersion"`
	StartedAt      string                   `json:"startedAt"`
	Subject        GovernanceSubjectBinding `json:"subject"`
}

type ApprovalArtifact struct {
	ArtifactID    string                   `json:"artifactId"`
	Conformance   GovernanceArtifactRef    `json:"conformance"`
	Decision      string                   `json:"decision"`
	DecisionHash  string                   `json:"decisionHash"`
	ExpiresAt     string                   `json:"expiresAt"`
	IssuedAt      string                   `json:"issuedAt"`
	SchemaVersion string                   `json:"schemaVersion"`
	Subject       GovernanceSubjectBinding `json:"subject"`
}

type ActivationArtifact struct {
	Approval           GovernanceArtifactRef    `json:"approval"`
	ArtifactID         string                   `json:"artifactId"`
	Conformance        GovernanceArtifactRef    `json:"conformance"`
	Decision           string                   `json:"decision"`
	DecisionHash       string                   `json:"decisionHash"`
	ExpiresAt          string                   `json:"expiresAt"`
	Fence              string                   `json:"fence"`
	Generation         uint64                   `json:"generation"`
	IssuedAt           string                   `json:"issuedAt"`
	PreviousFence      string                   `json:"previousFence"`
	PreviousGeneration uint64                   `json:"previousGeneration"`
	SchemaVersion      string                   `json:"schemaVersion"`
	Shadow             GovernanceArtifactRef    `json:"shadow"`
	Subject            GovernanceSubjectBinding `json:"subject"`
}

type ModelGovernanceReceipt struct {
	Activation    GovernanceArtifactRef    `json:"activation"`
	Approval      GovernanceArtifactRef    `json:"approval"`
	ArtifactID    string                   `json:"artifactId"`
	Conformance   GovernanceArtifactRef    `json:"conformance"`
	ExpiresAt     string                   `json:"expiresAt"`
	Fence         string                   `json:"fence"`
	Generation    uint64                   `json:"generation"`
	IssuedAt      string                   `json:"issuedAt"`
	SchemaVersion string                   `json:"schemaVersion"`
	Shadow        GovernanceArtifactRef    `json:"shadow"`
	Subject       GovernanceSubjectBinding `json:"subject"`
}

// GovernanceRevocationAuthorityBinding commits a Genesis decision to the
// exact current cumulative revocation ledger. The ID is a closed protocol
// constant rather than a caller-selected namespace.
type GovernanceRevocationAuthorityBinding struct {
	AuthorityHash string `json:"authorityHash"`
	AuthorityID   string `json:"authorityId"`
	Epoch         uint64 `json:"epoch"`
}

// GovernanceGenesisArtifact is the only signed decision allowed to create
// generation one. It intentionally has no ShadowArtifact: there is no prior
// workload head to compare against.
type GovernanceGenesisArtifact struct {
	Approval            GovernanceArtifactRef                `json:"approval"`
	ArtifactID          string                               `json:"artifactId"`
	Conformance         GovernanceArtifactRef                `json:"conformance"`
	Decision            string                               `json:"decision"`
	DecisionHash        string                               `json:"decisionHash"`
	ExpiresAt           string                               `json:"expiresAt"`
	Fence               string                               `json:"fence"`
	Generation          uint64                               `json:"generation"`
	IssuedAt            string                               `json:"issuedAt"`
	PreviousFence       string                               `json:"previousFence"`
	PreviousGeneration  uint64                               `json:"previousGeneration"`
	RevocationAuthority GovernanceRevocationAuthorityBinding `json:"revocationAuthority"`
	SchemaVersion       string                               `json:"schemaVersion"`
	Subject             GovernanceSubjectBinding             `json:"subject"`
}

// ModelGovernanceGenesisReceipt is a distinct receipt payload. The ordinary
// receipt verifier cannot parse it, and the Genesis verifier cannot parse an
// ordinary activation receipt.
type ModelGovernanceGenesisReceipt struct {
	Approval            GovernanceArtifactRef                `json:"approval"`
	ArtifactID          string                               `json:"artifactId"`
	Conformance         GovernanceArtifactRef                `json:"conformance"`
	ExpiresAt           string                               `json:"expiresAt"`
	Fence               string                               `json:"fence"`
	Generation          uint64                               `json:"generation"`
	Genesis             GovernanceArtifactRef                `json:"genesis"`
	IssuedAt            string                               `json:"issuedAt"`
	RevocationAuthority GovernanceRevocationAuthorityBinding `json:"revocationAuthority"`
	SchemaVersion       string                               `json:"schemaVersion"`
	Subject             GovernanceSubjectBinding             `json:"subject"`
}

// ProviderRouteAuthority contains commitments only. It is not a URL or a
// network permission. A runtime data plane must map EndpointDigest through its
// separately configured egress and TLS authorities.
type ProviderRouteAuthority struct {
	EgressPolicyHash string `json:"egressPolicyHash"`
	EndpointDigest   string `json:"endpointDigest"`
	ExpiresAt        string `json:"expiresAt"`
	IssuedAt         string `json:"issuedAt"`
	Protocol         string `json:"protocol"`
	RouteID          string `json:"routeId"`
	SchemaVersion    string `json:"schemaVersion"`
	TLSIdentityHash  string `json:"tlsIdentityHash"`
}

// GovernanceMaterials are immutable raw documents. Loaders must return exact
// bytes and never synthesize a missing artifact.
type GovernanceMaterials struct {
	ModelProfileJSON           []byte
	FrozenCorpusJSON           []byte
	ProviderRouteAuthorityJSON []byte
	ConformanceEnvelope        []byte
	ShadowEnvelope             []byte
	ApprovalEnvelope           []byte
	ActivationEnvelope         []byte
	ReceiptEnvelope            []byte
}

// GenesisGovernanceMaterials contains only the authorities meaningful for an
// empty workload head. In particular, it cannot smuggle a fake Shadow or an
// ordinary Activation decision into bootstrap verification.
type GenesisGovernanceMaterials struct {
	ModelProfileJSON           []byte
	FrozenCorpusJSON           []byte
	ProviderRouteAuthorityJSON []byte
	ConformanceEnvelope        []byte
	ApprovalEnvelope           []byte
	GenesisEnvelope            []byte
	ReceiptEnvelope            []byte
}

type GovernanceSignerTrust struct {
	Identity  string
	Role      string
	PublicKey ed25519.PublicKey
	NotBefore time.Time
	NotAfter  time.Time
}

type GovernanceRevocation struct {
	Digest     string
	ReasonHash string
	RevokedAt  time.Time
}

type GovernanceSignerRevocation struct {
	PolicyHash    string
	KeyID         string
	PublicKeyHash string
	ReasonHash    string
	RevokedAt     time.Time
}

// GovernanceTrustPolicy is the immutable signer policy bound by every signed
// subject. Operational revocations deliberately live in a separately hashed,
// short-lived authority so adding a revocation does not replace the signer
// policy hash and invalidate every unrelated receipt.
type GovernanceTrustPolicy struct {
	PolicyHash string
	Signers    map[string]GovernanceSignerTrust
}

// GovernanceRevocationAuthority is a current, cumulative authority supplied by
// the trusted control plane. AuthorityHash omits itself and commits the epoch,
// validity window, digest revocations, and signer-key revocations.
type GovernanceRevocationAuthority struct {
	AuthorityHash     string
	Epoch             uint64
	IssuedAt          time.Time
	ExpiresAt         time.Time
	DigestRevocations []GovernanceRevocation
	SignerRevocations []GovernanceSignerRevocation
}

type VerifiedGovernance struct {
	AuthorityKind         string
	Profile               ModelProfile
	Corpus                FrozenCorpus
	ProviderRoute         ProviderRouteAuthority
	Subject               GovernanceSubjectBinding
	Conformance           ConformanceArtifact
	Shadow                ShadowArtifact
	Approval              ApprovalArtifact
	Activation            ActivationArtifact
	Receipt               ModelGovernanceReceipt
	ConformanceRef        GovernanceArtifactRef
	ShadowRef             GovernanceArtifactRef
	ApprovalRef           GovernanceArtifactRef
	ActivationRef         GovernanceArtifactRef
	ReceiptEnvelopeDigest string
	ReceiptPayloadDigest  string
	SignerIdentities      map[string]string
	Genesis               GovernanceGenesisArtifact
	GenesisReceipt        ModelGovernanceGenesisReceipt
	GenesisRef            GovernanceArtifactRef
}
