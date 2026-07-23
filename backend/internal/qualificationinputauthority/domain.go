// Package qualificationinputauthority composes the two immutable input edges
// that Workflow Input Authority, Qualification Policy v1, and Qualification
// Plan v1 cannot represent on their own.
//
// The package is a non-secret control-plane contract. It does not issue
// credentials, approve Promotion, create a workflow handoff, or expose a
// browser-authored authority constructor.
package qualificationinputauthority

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	IssueRequestSchemaV1          = "worksflow-qualification-input-precommit-request/v1"
	SourceRequestSchemaV1         = "worksflow-qualification-source-verification-request/v1"
	CredentialRequestSchemaV1     = "worksflow-qualification-credential-resolution-request/v1"
	ReceiptAdmissionSchemaV1      = "worksflow-qualification-input-verification-receipt-admission/v1"
	AuthoritySchemaV1             = "worksflow-qualification-input-precommit-authority/v1"
	SourceTreeDigestSchemaV1      = "worksflow-source-content-tree/v1"
	AuthorityHashPrefixV1         = "worksflow-qualification-input-precommit-hash/v1"
	IssueRequestHashDomainV1      = "worksflow.qualification-input-precommit.request/v1"
	SourceRequestHashDomainV1     = "worksflow.qualification-input-precommit.source-request/v1"
	CredentialRequestHashDomainV1 = "worksflow.qualification-input-precommit.credential-request/v1"
	ReceiptAdmissionHashDomainV1  = "worksflow.qualification-input-precommit.receipt-admission/v1"
	AuthorityHashDomainV1         = "worksflow.qualification-input-precommit.authority/v1"

	PolicyStatusActive           = "active"
	ReceiptKindSource            = "source-verification"
	ReceiptKindCredential        = "credential-resolution"
	PromotionBindingKindV1       = "qualification-input-precommit"
	MaximumCanonicalBytes        = 4 << 20
	MaximumJavaScriptSafeInteger = int64(9007199254740991)
)

var (
	ErrInvalid             = errors.New("qualification input precommit authority is invalid")
	ErrNotFound            = errors.New("qualification input precommit authority is not found")
	ErrNotReady            = errors.New("qualification input precommit prerequisites are not ready")
	ErrStale               = errors.New("qualification input precommit upstream authority is stale")
	ErrConflict            = errors.New("qualification input precommit conflicts with immutable state")
	ErrRetryable           = errors.New("qualification input precommit encountered retryable database contention; retry the exact operation")
	ErrStoreOutcomeUnknown = errors.New("qualification input precommit store commit outcome is unknown")
	ErrOutcomeUnknown      = errors.New("qualification input precommit outcome is unknown; inspect the same operation")
)

// IssueCommand is server-owned. OperationID and AuthorityID are allocated by
// the service layer; the three upstream IDs are selected from trusted server
// state rather than accepted as canonical material from a browser.
type IssueCommand struct {
	OperationID                    uuid.UUID
	AuthorityID                    uuid.UUID
	WorkflowInputAuthorityID       uuid.UUID
	QualificationPolicyAuthorityID uuid.UUID
	QualificationPlanAuthorityID   uuid.UUID
}

type IssueRequest struct {
	AuthorityID                    string `json:"authorityId"`
	OperationID                    string `json:"operationId"`
	QualificationPlanAuthorityID   string `json:"qualificationPlanAuthorityId"`
	QualificationPolicyAuthorityID string `json:"qualificationPolicyAuthorityId"`
	SchemaVersion                  string `json:"schemaVersion"`
	WorkflowInputAuthorityID       string `json:"workflowInputAuthorityId"`
}

type AuthorityReference struct {
	AuthorityHash string `json:"authorityHash"`
	AuthorityID   string `json:"authorityId"`
}

type WorkflowInputBinding struct {
	AuthorityHash                    string `json:"authorityHash"`
	AuthorityID                      string `json:"authorityId"`
	InputHash                        string `json:"inputHash"`
	QualificationPolicyAuthorityHash string `json:"qualificationPolicyAuthorityHash"`
	QualificationPolicyAuthorityID   string `json:"qualificationPolicyAuthorityId"`
}

// CredentialProfile is the complete non-secret Policy v1 projection. The
// MemberRequestSetDigest is a reviewed request closure, not a credential-set
// member binding digest.
type CredentialProfile struct {
	Audience               string `json:"audience"`
	AuthorityID            string `json:"authorityId"`
	IssuanceArtifactID     string `json:"issuanceArtifactId"`
	MemberRequestSetDigest string `json:"memberRequestSetDigest"`
	RevocationArtifactID   string `json:"revocationArtifactId"`
}

type PolicyBinding struct {
	AuthorityHash        string            `json:"authorityHash"`
	AuthorityID          string            `json:"authorityId"`
	CredentialProfile    CredentialProfile `json:"credentialProfile"`
	PlanInputProfileHash string            `json:"planInputProfileHash"`
	SourcePolicyDigest   string            `json:"sourcePolicyDigest"`
}

type SourceProjection struct {
	Commit           string `json:"commit"`
	Dirty            bool   `json:"dirty"`
	TreeDigest       string `json:"treeDigest"`
	TreeDigestSchema string `json:"treeDigestSchema"`
}

// CredentialSetProjection is the complete non-secret Plan v1 credential
// expectation. It contains hashes and artifact identities, never members or
// bearer material.
type CredentialSetProjection struct {
	Audience             string `json:"audience"`
	IssuanceArtifactID   string `json:"issuanceArtifactId"`
	Issuer               string `json:"issuer"`
	MemberBindingsDigest string `json:"memberBindingsDigest"`
	MemberCount          int    `json:"memberCount"`
	RevocationArtifactID string `json:"revocationArtifactId"`
	SetHandleHash        string `json:"setHandleHash"`
	SetID                string `json:"setId"`
}

type PlanBinding struct {
	AuthorityHash    string                  `json:"authorityHash"`
	AuthorityID      string                  `json:"authorityId"`
	CredentialSet    CredentialSetProjection `json:"credentialSet"`
	InputAuthorityID string                  `json:"inputAuthorityId"`
	InputHash        string                  `json:"inputHash"`
	Source           SourceProjection        `json:"source"`
}

type SourceVerificationRequest struct {
	Plan               AuthorityReference `json:"plan"`
	Policy             AuthorityReference `json:"policy"`
	SchemaVersion      string             `json:"schemaVersion"`
	Source             SourceProjection   `json:"source"`
	SourcePolicyDigest string             `json:"sourcePolicyDigest"`
	Verifier           ExecutableBinding  `json:"verifier"`
	WorkflowInput      AuthorityReference `json:"workflowInput"`
}

type CredentialResolutionRequest struct {
	CredentialProfile CredentialProfile       `json:"credentialProfile"`
	CredentialSet     CredentialSetProjection `json:"credentialSet"`
	Plan              AuthorityReference      `json:"plan"`
	Policy            AuthorityReference      `json:"policy"`
	Resolver          ExecutableBinding       `json:"resolver"`
	SchemaVersion     string                  `json:"schemaVersion"`
	WorkflowInput     AuthorityReference      `json:"workflowInput"`
}

// ExecutableBinding identifies the separately deployed server authority and
// the reviewed executable image/binary digest used for one role.
type ExecutableBinding struct {
	AuthorityID      string `json:"authorityId"`
	ExecutableDigest string `json:"executableDigest"`
}

type VerificationProof struct {
	AdmissionHash    string `json:"admissionHash"`
	AuthorityID      string `json:"authorityId"`
	ExecutableDigest string `json:"executableDigest"`
	ReceiptHash      string `json:"receiptHash"`
	RequestHash      string `json:"requestHash"`
}

// ReceiptAdmission is the package-owned immutable proof that one sealed
// adapter verified an external receipt for one exact component request. The
// later SQL Store must persist and resolve this document; the precommit row is
// forbidden from trusting a candidate ReceiptHash string by itself.
type ReceiptAdmission struct {
	AuthorityID      string `json:"authorityId"`
	ExecutableDigest string `json:"executableDigest"`
	Kind             string `json:"kind"`
	ReceiptHash      string `json:"receiptHash"`
	RequestHash      string `json:"requestHash"`
	SchemaVersion    string `json:"schemaVersion"`
}

type ReceiptAdmissionRecord struct {
	Document      ReceiptAdmission
	DocumentBytes []byte
	RequestBytes  []byte
	AdmissionHash string
}

// VerificationObservation is the only result a production adapter returns.
// The sealed adapter converts it to a package-private grant after checking the
// exact request hash.
type VerificationObservation struct {
	ReceiptHash string
	RequestHash string
}

type AuthorityDocument struct {
	AuthorityID           string               `json:"authorityId"`
	CredentialProof       VerificationProof    `json:"credentialProof"`
	CredentialRequestHash string               `json:"credentialRequestHash"`
	IssuedAt              string               `json:"issuedAt"`
	OperationID           string               `json:"operationId"`
	Plan                  PlanBinding          `json:"plan"`
	Policy                PolicyBinding        `json:"policy"`
	RequestHash           string               `json:"requestHash"`
	SchemaVersion         string               `json:"schemaVersion"`
	SourceProof           VerificationProof    `json:"sourceProof"`
	SourceRequestHash     string               `json:"sourceRequestHash"`
	WorkflowInput         WorkflowInputBinding `json:"workflowInput"`
}

// PromotionBinding is the exact fixed projection that a later Promotion
// closure must embed and hash. It is intentionally richer than the current
// optional independent-authority projection: the precommit identity is
// generated after Policy issuance and therefore cannot be an opaque ID/hash
// chosen in Policy v1.
type PromotionBinding struct {
	AuthorityHash                    string `json:"authorityHash"`
	AuthorityID                      string `json:"authorityId"`
	CredentialAdmissionHash          string `json:"credentialAdmissionHash"`
	CredentialReceiptHash            string `json:"credentialReceiptHash"`
	CredentialRequestHash            string `json:"credentialRequestHash"`
	Kind                             string `json:"kind"`
	QualificationPlanAuthorityHash   string `json:"qualificationPlanAuthorityHash"`
	QualificationPlanAuthorityID     string `json:"qualificationPlanAuthorityId"`
	QualificationPolicyAuthorityHash string `json:"qualificationPolicyAuthorityHash"`
	QualificationPolicyAuthorityID   string `json:"qualificationPolicyAuthorityId"`
	SourceAdmissionHash              string `json:"sourceAdmissionHash"`
	SourceReceiptHash                string `json:"sourceReceiptHash"`
	SourceRequestHash                string `json:"sourceRequestHash"`
	WorkflowInputAuthorityHash       string `json:"workflowInputAuthorityHash"`
	WorkflowInputAuthorityID         string `json:"workflowInputAuthorityId"`
}

// ResolvedAuthorities is a server-owned projection. PolicyCurrent and
// PolicyStatus are transaction conditions and are not caller-authored members
// of the immutable wire. Production Store.Issue must independently re-resolve
// and compare all included fields while holding its locks.
type ResolvedAuthorities struct {
	CredentialResolver ExecutableBinding
	Plan               PlanBinding
	Policy             PolicyBinding
	PolicyCurrent      bool
	PolicyStatus       string
	SourceVerifier     ExecutableBinding
	WorkflowInput      WorkflowInputBinding
}

type Record struct {
	Command IssueCommand

	Request      IssueRequest
	RequestBytes []byte
	RequestHash  string

	SourceRequest      SourceVerificationRequest
	SourceRequestBytes []byte
	SourceRequestHash  string

	CredentialRequest      CredentialResolutionRequest
	CredentialRequestBytes []byte
	CredentialRequestHash  string

	Document      AuthorityDocument
	DocumentBytes []byte
	AuthorityHash string

	IssuedAt   time.Time
	Idempotent bool
}

type AuthorityResolver interface {
	Resolve(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (ResolvedAuthorities, error)
}

// SourceVerifier and CredentialResolver are intentionally sealed. Production
// adapters are installed with the constructors below; only this package can
// receive their package-private grants.
type SourceVerifier interface {
	sourceBinding() ExecutableBinding
	verifySource(context.Context, SourceVerificationRequest, []byte, string) (verifiedSourceGrant, error)
}

type CredentialResolver interface {
	credentialBinding() ExecutableBinding
	resolveCredential(context.Context, CredentialResolutionRequest, []byte, string) (verifiedCredentialGrant, error)
}

type SourceVerificationFunc func(context.Context, SourceVerificationRequest, []byte, string) (VerificationObservation, error)

type CredentialResolutionFunc func(context.Context, CredentialResolutionRequest, []byte, string) (VerificationObservation, error)

type DatabaseClock interface {
	Now(context.Context) (time.Time, error)
}

type DatabaseClockFunc func(context.Context) (time.Time, error)

func (function DatabaseClockFunc) Now(ctx context.Context) (time.Time, error) {
	return function(ctx)
}

// Store.Issue must atomically re-resolve the exact current upstream tuple and
// append the immutable record. It must not trust a series of autocommit reads
// performed by Service.
type Store interface {
	admitSourceReceipt(context.Context, verifiedSourceGrant) (ReceiptAdmissionRecord, error)
	admitCredentialReceipt(context.Context, verifiedCredentialGrant) (ReceiptAdmissionRecord, error)
	resolveReceiptAdmission(context.Context, string, string) (ReceiptAdmissionRecord, error)
	resolveReceiptAdmissionForRequest(context.Context, string, string) (ReceiptAdmissionRecord, error)
	Issue(context.Context, Record) (Record, error)
	InspectOperation(context.Context, uuid.UUID) (Record, error)
	ResolveAuthority(context.Context, uuid.UUID) (Record, error)
}

type verifiedSourceGrant struct {
	proof        VerificationProof
	requestBytes []byte
}

type verifiedCredentialGrant struct {
	proof        VerificationProof
	requestBytes []byte
}
