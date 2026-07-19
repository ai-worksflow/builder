// Package qualificationevidence coordinates the non-secret control plane for
// producing one immutable external-qualification evidence snapshot.
//
// It is intentionally an orchestration contract, not a production adapter.
// Real credential brokers, capture services, encryptors, KMS authorities,
// signers, sealers, and target evidence verifiers must be injected at startup.
package qualificationevidence

import (
	"context"
	"errors"
	"time"
)

const (
	PlanSchemaV1                   = "worksflow-qualification-evidence-plan/v1"
	CredentialMemberBindingsSchema = "worksflow-credential-set-member-bindings/v1"
	EncryptionManifestSchemaV1     = "worksflow-qualification-encryption-manifest/v1"
	PreKMSArtifactSetSchemaV1      = "worksflow-qualification-pre-kms-artifact-set/v1"
	ArtifactSetSchemaV1            = "worksflow-qualification-evidence-artifact-set/v1"
	EvidenceClosureSchemaV1        = "worksflow-qualification-evidence-closure/v1"
	ImmutableSnapshotMode          = "immutable-filesystem"

	MaximumArtifacts = 512
	MaximumMembers   = 64
)

var (
	ErrInvalid              = errors.New("qualification evidence input or observation is invalid")
	ErrNotFound             = errors.New("qualification evidence orchestration is not found")
	ErrIdempotencyConflict  = errors.New("qualification evidence idempotency identity conflicts with prior input")
	ErrCASConflict          = errors.New("qualification evidence state compare-and-swap conflict")
	ErrInvalidTransition    = errors.New("qualification evidence state transition is invalid")
	ErrOutcomeUnknown       = errors.New("qualification evidence operation outcome is unknown; inspect the same operation")
	ErrStoreOutcomeUnknown  = errors.New("qualification evidence store commit outcome is unknown")
	ErrExternalRejected     = errors.New("trusted qualification evidence authority rejected the operation")
	ErrEvidenceClosure      = errors.New("qualification evidence artifact closure is not exact")
	ErrDigestDrift          = errors.New("qualification evidence digest drifted from the reserved closure")
	ErrCredentialDrift      = errors.New("qualification evidence credential set is not the exact issued set")
	ErrPlaintextDisposition = errors.New("qualification evidence plaintext disposition is incomplete")
)

type Classification string

const (
	ClassificationDistributable Classification = "distributable"
	ClassificationRestricted    Classification = "restricted"
)

type ArtifactKind string

const (
	ArtifactKindRunResult    ArtifactKind = "run-result"
	ArtifactKindTrace        ArtifactKind = "trace"
	ArtifactKindVideo        ArtifactKind = "video"
	ArtifactKindLog          ArtifactKind = "log"
	ArtifactKindGolden       ArtifactKind = "golden-document"
	ArtifactKindFault        ArtifactKind = "fault-evidence"
	ArtifactKindWriterDrain  ArtifactKind = "writer-drain-proof"
	ArtifactKindRuntimeProof ArtifactKind = "runtime-proof"
)

type PlaintextDisposition string

const (
	PlaintextNeverPersisted PlaintextDisposition = "never-persisted"
	PlaintextDeleted        PlaintextDisposition = "deleted"
)

type AuthorityStage string

const (
	AuthorityPending   AuthorityStage = "pending"
	AuthorityCommitted AuthorityStage = "committed"
	AuthorityRejected  AuthorityStage = "rejected"
)

// TrustBindings are server configuration, not request fields. Each authority
// identity must be distinct so one adapter cannot silently stand in for a
// different qualification role.
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

// Plan is the complete, non-secret reservation identity. It has no generic
// metadata, URL, header, filesystem path, or arbitrary environment field.
type Plan struct {
	SchemaVersion   string `json:"schemaVersion"`
	OrchestrationID string `json:"orchestrationId"`
	RunID           string `json:"runId"`
	FixtureID       string `json:"fixtureId"`
	// QualificationPlanArtifactID and PlanDigest bind an external immutable
	// worksflow qualification-plan artifact. PlanDigest is not a digest of this
	// orchestration Plan (whose idempotency digest is stored separately).
	QualificationPlanArtifactID string                `json:"qualificationPlanArtifactId"`
	PlanDigest                  string                `json:"planDigest"`
	SourceTreeDigest            string                `json:"sourceTreeDigest"`
	TemplateReleaseDigest       string                `json:"templateReleaseDigest"`
	Operations                  OperationIDs          `json:"operations"`
	CredentialSet               CredentialExpectation `json:"credentialSet"`
	Artifacts                   []ArtifactExpectation `json:"artifacts"`
	Recipient                   EncryptionRecipient   `json:"recipient"`
	Outputs                     OutputExpectation     `json:"outputs"`
}

type OperationIDs struct {
	Reserve              string `json:"reserve"`
	CredentialIssue      string `json:"credentialIssue"`
	RunClosure           string `json:"runClosure"`
	KMSAttestation       string `json:"kmsAttestation"`
	CredentialRevocation string `json:"credentialRevocation"`
	ArtifactIndex        string `json:"artifactIndex"`
	ReceiptSign          string `json:"receiptSign"`
	SnapshotSeal         string `json:"snapshotSeal"`
}

type CredentialExpectation struct {
	SetID                string `json:"setId"`
	Issuer               string `json:"issuer"`
	Audience             string `json:"audience"`
	SetHandleHash        string `json:"setHandleHash"`
	MemberBindingsDigest string `json:"memberBindingsDigest"`
	MemberCount          int    `json:"memberCount"`
	IssuanceArtifactID   string `json:"issuanceArtifactId"`
	RevocationArtifactID string `json:"revocationArtifactId"`
}

type ArtifactExpectation struct {
	ID                    string         `json:"id"`
	Kind                  ArtifactKind   `json:"kind"`
	Classification        Classification `json:"classification"`
	EncryptionOperationID string         `json:"encryptionOperationId"`
}

type EncryptionRecipient struct {
	KeyResourceID string `json:"keyResourceId"`
	KeyVersion    string `json:"keyVersion"`
}

type OutputExpectation struct {
	KMSAttestationArtifactID string `json:"kmsAttestationArtifactId"`
	ArtifactIndexID          string `json:"artifactIndexId"`
	ReceiptID                string `json:"receiptId"`
	SnapshotID               string `json:"snapshotId"`
}

type CredentialMember struct {
	Slot                 string `json:"slot"`
	ActorID              string `json:"actorId"`
	Kind                 string `json:"kind"`
	CredentialHandleHash string `json:"credentialHandleHash"`
}

// CredentialSetBinding contains one-way commitments only. Members are kept so
// revocation can be proven to cover the exact issued set, not merely a count.
type CredentialSetBinding struct {
	SetID                string             `json:"setId"`
	RunID                string             `json:"runId"`
	FixtureID            string             `json:"fixtureId"`
	Issuer               string             `json:"issuer"`
	Audience             string             `json:"audience"`
	SetHandleHash        string             `json:"setHandleHash"`
	MemberBindingsDigest string             `json:"memberBindingsDigest"`
	MemberCount          int                `json:"memberCount"`
	Members              []CredentialMember `json:"members"`
	IssuedAt             string             `json:"issuedAt"`
	ExpiresAt            string             `json:"expiresAt"`
}

type SignedArtifact struct {
	ID                string `json:"id"`
	ContentDigest     string `json:"contentDigest"`
	PayloadDigest     string `json:"payloadDigest"`
	SignerSetDigest   string `json:"signerSetDigest"`
	SignerCount       int    `json:"signerCount"`
	AuthorityIdentity string `json:"authorityIdentity"`
	IssuedAt          string `json:"issuedAt"`
}

type CredentialIssueRequest struct {
	OperationID string                `json:"operationId"`
	RunID       string                `json:"runId"`
	FixtureID   string                `json:"fixtureId"`
	PlanDigest  string                `json:"planDigest"`
	Expected    CredentialExpectation `json:"expected"`
}

type CredentialIssueObservation struct {
	OperationID   string               `json:"operationId"`
	RequestDigest string               `json:"requestDigest"`
	Stage         AuthorityStage       `json:"stage"`
	Binding       CredentialSetBinding `json:"binding"`
	Attestation   SignedArtifact       `json:"attestation"`
}

type OperationRef struct {
	OperationID     string `json:"operationId"`
	OrchestrationID string `json:"orchestrationId"`
	RunID           string `json:"runId"`
}

type CapturedArtifact struct {
	ID             string         `json:"id"`
	Kind           ArtifactKind   `json:"kind"`
	Classification Classification `json:"classification"`
	CaptureRef     string         `json:"captureRef"`
	ContentDigest  string         `json:"contentDigest"`
	SizeBytes      int64          `json:"sizeBytes"`
}

type RunClosureRequest struct {
	OperationID                string `json:"operationId"`
	RunID                      string `json:"runId"`
	PlanDigest                 string `json:"planDigest"`
	CredentialSetBindingDigest string `json:"credentialSetBindingDigest"`
	ExpectedArtifactSetDigest  string `json:"expectedArtifactSetDigest"`
}

type RunClosureObservation struct {
	OperationID   string             `json:"operationId"`
	RequestDigest string             `json:"requestDigest"`
	AuthorityID   string             `json:"authorityId"`
	Stage         AuthorityStage     `json:"stage"`
	ResultDigest  string             `json:"resultDigest"`
	CaptureDigest string             `json:"captureDigest"`
	CompletedAt   string             `json:"completedAt"`
	Artifacts     []CapturedArtifact `json:"artifacts"`
}

type EncryptionRequest struct {
	OperationID        string              `json:"operationId"`
	RunID              string              `json:"runId"`
	PlanDigest         string              `json:"planDigest"`
	Artifact           CapturedArtifact    `json:"artifact"`
	Recipient          EncryptionRecipient `json:"recipient"`
	AdditionalDataHash string              `json:"additionalDataHash"`
}

type EncryptionCommitment struct {
	OperationID                string               `json:"operationId"`
	RequestDigest              string               `json:"requestDigest"`
	AuthorityID                string               `json:"authorityId"`
	Stage                      AuthorityStage       `json:"stage"`
	ArtifactID                 string               `json:"artifactId"`
	PlaintextDigest            string               `json:"plaintextDigest"`
	CiphertextRef              string               `json:"ciphertextRef"`
	CiphertextDigest           string               `json:"ciphertextDigest"`
	SizeBytes                  int64                `json:"sizeBytes"`
	EncryptionDescriptorDigest string               `json:"encryptionDescriptorDigest"`
	WrappedKeyDigest           string               `json:"wrappedKeyDigest"`
	AdditionalDataHash         string               `json:"additionalDataHash"`
	Recipient                  EncryptionRecipient  `json:"recipient"`
	EncryptedAt                string               `json:"encryptedAt"`
	PlaintextDisposition       PlaintextDisposition `json:"plaintextDisposition"`
	PlaintextDispositionAt     string               `json:"plaintextDispositionAt"`
}

type KMSAttestationRequest struct {
	OperationID           string `json:"operationId"`
	RunID                 string `json:"runId"`
	PlanDigest            string `json:"planDigest"`
	ManifestDigest        string `json:"manifestDigest"`
	ArtifactSetDigest     string `json:"artifactSetDigest"`
	ArtifactCount         int    `json:"artifactCount"`
	ExpectedArtifactID    string `json:"expectedArtifactId"`
	ExpectedPayloadDigest string `json:"expectedPayloadDigest"`
}

type KMSAttestationObservation struct {
	OperationID       string         `json:"operationId"`
	RequestDigest     string         `json:"requestDigest"`
	AuthorityID       string         `json:"authorityId"`
	Stage             AuthorityStage `json:"stage"`
	ManifestDigest    string         `json:"manifestDigest"`
	ArtifactSetDigest string         `json:"artifactSetDigest"`
	Attestation       SignedArtifact `json:"attestation"`
}

type CredentialRevocationRequest struct {
	OperationID          string               `json:"operationId"`
	RunID                string               `json:"runId"`
	Binding              CredentialSetBinding `json:"binding"`
	KMSAttestationDigest string               `json:"kmsAttestationDigest"`
}

type CredentialRevocationObservation struct {
	OperationID          string               `json:"operationId"`
	RequestDigest        string               `json:"requestDigest"`
	KMSAttestationDigest string               `json:"kmsAttestationDigest"`
	Stage                AuthorityStage       `json:"stage"`
	Binding              CredentialSetBinding `json:"binding"`
	RevokedAt            string               `json:"revokedAt"`
	Attestation          SignedArtifact       `json:"attestation"`
}

type ArtifactIndexRequest struct {
	OperationID             string `json:"operationId"`
	RunID                   string `json:"runId"`
	PlanDigest              string `json:"planDigest"`
	EvidenceClosureDigest   string `json:"evidenceClosureDigest"`
	ArtifactSetDigest       string `json:"artifactSetDigest"`
	ArtifactCount           int    `json:"artifactCount"`
	RestrictedArtifactCount int    `json:"restrictedArtifactCount"`
	ExpectedIndexID         string `json:"expectedIndexId"`
}

type ArtifactIndexCommitment struct {
	OperationID             string         `json:"operationId"`
	RequestDigest           string         `json:"requestDigest"`
	AuthorityID             string         `json:"authorityId"`
	Stage                   AuthorityStage `json:"stage"`
	IndexID                 string         `json:"indexId"`
	ContentDigest           string         `json:"contentDigest"`
	EvidenceClosureDigest   string         `json:"evidenceClosureDigest"`
	ArtifactSetDigest       string         `json:"artifactSetDigest"`
	ArtifactCount           int            `json:"artifactCount"`
	RestrictedArtifactCount int            `json:"restrictedArtifactCount"`
}

type ReceiptSignRequest struct {
	OperationID           string                  `json:"operationId"`
	RunID                 string                  `json:"runId"`
	PlanDigest            string                  `json:"planDigest"`
	EvidenceClosureDigest string                  `json:"evidenceClosureDigest"`
	Index                 ArtifactIndexCommitment `json:"index"`
	ExpectedReceiptID     string                  `json:"expectedReceiptId"`
	ExpectedPayloadDigest string                  `json:"expectedPayloadDigest"`
}

type QualificationReceiptCommitment struct {
	OperationID           string         `json:"operationId"`
	RequestDigest         string         `json:"requestDigest"`
	AuthorityID           string         `json:"authorityId"`
	Stage                 AuthorityStage `json:"stage"`
	ReceiptID             string         `json:"receiptId"`
	ContentDigest         string         `json:"contentDigest"`
	PayloadDigest         string         `json:"payloadDigest"`
	SubjectIndexDigest    string         `json:"subjectIndexDigest"`
	EvidenceClosureDigest string         `json:"evidenceClosureDigest"`
	SignerSetDigest       string         `json:"signerSetDigest"`
	SignerCount           int            `json:"signerCount"`
	IssuedAt              string         `json:"issuedAt"`
}

type SnapshotSealRequest struct {
	OperationID           string                         `json:"operationId"`
	RunID                 string                         `json:"runId"`
	EvidenceClosureDigest string                         `json:"evidenceClosureDigest"`
	Index                 ArtifactIndexCommitment        `json:"index"`
	Receipt               QualificationReceiptCommitment `json:"receipt"`
	ExpectedSnapshotID    string                         `json:"expectedSnapshotId"`
	Mode                  string                         `json:"mode"`
}

type SnapshotCommitment struct {
	OperationID           string         `json:"operationId"`
	RequestDigest         string         `json:"requestDigest"`
	AuthorityID           string         `json:"authorityId"`
	Stage                 AuthorityStage `json:"stage"`
	SnapshotID            string         `json:"snapshotId"`
	SnapshotDigest        string         `json:"snapshotDigest"`
	EvidenceClosureDigest string         `json:"evidenceClosureDigest"`
	IndexDigest           string         `json:"indexDigest"`
	ReceiptDigest         string         `json:"receiptDigest"`
	Mode                  string         `json:"mode"`
	SealedAt              string         `json:"sealedAt"`
}

type SnapshotVerificationRequest struct {
	OrchestrationID       string             `json:"orchestrationId"`
	RunID                 string             `json:"runId"`
	EvidenceClosureDigest string             `json:"evidenceClosureDigest"`
	Snapshot              SnapshotCommitment `json:"snapshot"`
}

type SnapshotVerification struct {
	AuthorityID           string `json:"authorityId"`
	SnapshotID            string `json:"snapshotId"`
	SnapshotDigest        string `json:"snapshotDigest"`
	EvidenceClosureDigest string `json:"evidenceClosureDigest"`
	IndexDigest           string `json:"indexDigest"`
	ReceiptDigest         string `json:"receiptDigest"`
	VerifiedAt            string `json:"verifiedAt"`
}

// PlanAuthorityResolution is the immutable, server-owned envelope behind one
// opaque authority UUID. EvidencePlanBytes are the exact canonical bytes of
// Plan and EvidencePlanHash is their SHA-256 digest. AuthorityHash identifies
// the wider authority record whose resolver is responsible for validating the
// exact upstream and workflow target before returning this projection.
//
// Plan v1 does not itself contain that target. Consequently this envelope is
// sufficient only to enter the internal evidence lifecycle; it is not a
// qualification promotion authority or permission to submit a workflow node.
type PlanAuthorityResolution struct {
	AuthorityID         string `json:"authorityId"`
	AuthorityHash       string `json:"authorityHash"`
	ArtifactID          string `json:"artifactId"`
	EvidencePlanHash    string `json:"evidencePlanHash"`
	EvidencePlanBytes   []byte `json:"evidencePlanBytes"`
	TrustBindingsDigest string `json:"trustBindingsDigest"`
	Plan                Plan   `json:"plan"`
}

// PlanAuthority resolves an opaque UUID through trusted server storage. An
// implementation must fail closed unless its immutable authority record binds
// the exact project, WorkflowRun, node, target revision, source, build, and
// TemplateRelease lineage. Callers cannot substitute a Plan directly.
type PlanAuthority interface {
	Resolve(context.Context, string) (PlanAuthorityResolution, error)
}

// Every mutating boundary has a distinct read-only inspection method. Once a
// started event exists, Service uses only Inspect with that operation ID.
type CredentialSetAuthority interface {
	IssueAtomic(context.Context, CredentialIssueRequest) (CredentialIssueObservation, error)
	InspectIssue(context.Context, OperationRef) (CredentialIssueObservation, error)
	RevokeExact(context.Context, CredentialRevocationRequest) (CredentialRevocationObservation, error)
	InspectRevocation(context.Context, OperationRef) (CredentialRevocationObservation, error)
}

type RunCaptureAuthority interface {
	CloseRun(context.Context, RunClosureRequest) (RunClosureObservation, error)
	InspectRunClosure(context.Context, OperationRef) (RunClosureObservation, error)
}

type ArtifactEncryptor interface {
	Encrypt(context.Context, EncryptionRequest) (EncryptionCommitment, error)
	InspectEncryption(context.Context, OperationRef) (EncryptionCommitment, error)
}

type KMSAuthority interface {
	Attest(context.Context, KMSAttestationRequest) (KMSAttestationObservation, error)
	InspectAttestation(context.Context, OperationRef) (KMSAttestationObservation, error)
}

type ArtifactIndexer interface {
	BuildIndex(context.Context, ArtifactIndexRequest) (ArtifactIndexCommitment, error)
	InspectIndex(context.Context, OperationRef) (ArtifactIndexCommitment, error)
}

type ReceiptAuthority interface {
	SignReceipt(context.Context, ReceiptSignRequest) (QualificationReceiptCommitment, error)
	InspectReceipt(context.Context, OperationRef) (QualificationReceiptCommitment, error)
}

type SnapshotSealer interface {
	Seal(context.Context, SnapshotSealRequest) (SnapshotCommitment, error)
	InspectSeal(context.Context, OperationRef) (SnapshotCommitment, error)
}

type SnapshotVerifier interface {
	// Verify must be side-effect-free and read-only: it may reopen and hash the
	// immutable snapshot but must not sign, seal, publish, or mutate anything.
	// Therefore retry does not require a started event or Inspect operation.
	Verify(context.Context, SnapshotVerificationRequest) (SnapshotVerification, error)
}

type Phase string

const (
	PhaseReserved                    Phase = "reserved"
	PhaseCredentialIssueStarted      Phase = "credential-issue-started"
	PhaseCredentialIssued            Phase = "credential-issued"
	PhaseRunClosureStarted           Phase = "run-closure-started"
	PhaseRunClosureAccepted          Phase = "run-closure-accepted"
	PhaseEncrypting                  Phase = "encrypting"
	PhaseKMSAttestationStarted       Phase = "kms-attestation-started"
	PhaseKMSAttested                 Phase = "kms-attested"
	PhaseCredentialRevocationStarted Phase = "credential-revocation-started"
	PhaseCredentialRevoked           Phase = "credential-revoked"
	PhaseArtifactIndexStarted        Phase = "artifact-index-started"
	PhaseArtifactIndexed             Phase = "artifact-indexed"
	PhaseReceiptSignStarted          Phase = "receipt-sign-started"
	PhaseReceiptSigned               Phase = "receipt-signed"
	PhaseSnapshotSealStarted         Phase = "snapshot-seal-started"
	PhaseSnapshotSealed              Phase = "snapshot-sealed"
	PhaseComplete                    Phase = "complete"
)

type EventKind string

const (
	EventReserved                    EventKind = "reserved"
	EventCredentialIssueStarted      EventKind = "credential-issue-started"
	EventCredentialIssued            EventKind = "credential-issued"
	EventRunClosureStarted           EventKind = "run-closure-started"
	EventRunClosureAccepted          EventKind = "run-closure-accepted"
	EventEncryptionStarted           EventKind = "encryption-started"
	EventEncryptionCommitted         EventKind = "encryption-committed"
	EventKMSAttestationStarted       EventKind = "kms-attestation-started"
	EventKMSAttested                 EventKind = "kms-attested"
	EventCredentialRevocationStarted EventKind = "credential-revocation-started"
	EventCredentialRevoked           EventKind = "credential-revoked"
	EventArtifactIndexStarted        EventKind = "artifact-index-started"
	EventArtifactIndexed             EventKind = "artifact-indexed"
	EventReceiptSignStarted          EventKind = "receipt-sign-started"
	EventReceiptSigned               EventKind = "receipt-signed"
	EventSnapshotSealStarted         EventKind = "snapshot-seal-started"
	EventSnapshotSealed              EventKind = "snapshot-sealed"
	EventSnapshotVerified            EventKind = "snapshot-verified"
)

// Event has a closed shape. In particular it has no metadata, diagnostic text,
// request headers, response bodies, or paths where a secret could be copied.
type Event struct {
	At                   string                           `json:"at"`
	EventID              string                           `json:"eventId"`
	Kind                 EventKind                        `json:"kind"`
	OperationID          string                           `json:"operationId"`
	CommandHash          string                           `json:"commandHash"`
	TrustBindingsDigest  string                           `json:"trustBindingsDigest"`
	Plan                 *Plan                            `json:"plan"`
	CredentialIssue      *CredentialIssueObservation      `json:"credentialIssue"`
	RunClosure           *RunClosureObservation           `json:"runClosure"`
	Encryption           *EncryptionCommitment            `json:"encryption"`
	KMSAttestation       *KMSAttestationObservation       `json:"kmsAttestation"`
	CredentialRevocation *CredentialRevocationObservation `json:"credentialRevocation"`
	ArtifactIndex        *ArtifactIndexCommitment         `json:"artifactIndex"`
	Receipt              *QualificationReceiptCommitment  `json:"receipt"`
	Snapshot             *SnapshotCommitment              `json:"snapshot"`
	Verification         *SnapshotVerification            `json:"verification"`
}

type Snapshot struct {
	OrchestrationID      string
	CommandHash          string
	TrustBindingsDigest  string
	Plan                 *Plan
	Phase                Phase
	Version              uint64
	LastEventID          string
	LastEventAt          string
	ActiveOperationID    string
	ActiveArtifactID     string
	CredentialIssue      *CredentialIssueObservation
	RunClosure           *RunClosureObservation
	Encryptions          []EncryptionCommitment
	KMSAttestation       *KMSAttestationObservation
	CredentialRevocation *CredentialRevocationObservation
	ArtifactIndex        *ArtifactIndexCommitment
	Receipt              *QualificationReceiptCommitment
	SealedSnapshot       *SnapshotCommitment
	Verification         *SnapshotVerification
}

type Result struct {
	OrchestrationID       string                         `json:"orchestrationId"`
	RunID                 string                         `json:"runId"`
	EvidenceClosureDigest string                         `json:"evidenceClosureDigest"`
	ArtifactIndex         ArtifactIndexCommitment        `json:"artifactIndex"`
	Receipt               QualificationReceiptCommitment `json:"receipt"`
	Snapshot              SnapshotCommitment             `json:"snapshot"`
	Verification          SnapshotVerification           `json:"verification"`
}

// Store is append-only and strongly consistent after ErrStoreOutcomeUnknown.
// ErrCASConflict means the append definitely did not commit; outcome-unknown
// means callers must Load and reconcile the exact EventID.
type Store interface {
	TrustedTime(context.Context) (time.Time, error)
	Create(context.Context, string, Event) (Snapshot, bool, error)
	Load(context.Context, string) (Snapshot, error)
	Append(context.Context, string, uint64, Event) (Snapshot, error)
	Events(context.Context, string) ([]Event, error)
}

type Clock interface {
	Now() time.Time
}
