package qualificationreceiptv3

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	ControlRequestSchemaV1            = "worksflow-qualification-receipt-control-request/v1"
	ControlObservationPayloadSchemaV1 = "worksflow-qualification-receipt-control-observation-payload/v1"
	ControlObservationProofSchemaV1   = "worksflow-qualification-receipt-control-observation-proof/v1"
	ControlObservationPayloadType     = "application/vnd.worksflow.qualification-receipt-control-observation+json"
	ControlAuthenticationEd25519      = "ed25519"
	ControlClaimTokenSchemaV1         = "worksflow-qualification-receipt-control-claim/v1"
	ControlAcknowledgementSchemaV1    = "worksflow-qualification-receipt-control-acknowledgement/v1"
	ControlCompletionSchemaV1         = "worksflow-qualification-receipt-control-completion/v1"

	RequestKindSnapshotSeal   RequestKind = "snapshot-seal"
	RequestKindSnapshotVerify RequestKind = "snapshot-verify"
	RequestKindReceiptSign    RequestKind = "receipt-sign"

	ControlRoleSealer          ControlRole = "sealer"
	ControlRoleVerifier        ControlRole = "verifier"
	ControlRoleRunner          ControlRole = SignerRoleRunner
	ControlRoleReleaseApprover ControlRole = SignerRoleApprover

	ObservationPending    ObservationStatus = "pending"
	ObservationNotInvoked ObservationStatus = "not-invoked"
	ObservationCommitted  ObservationStatus = "committed"
	ObservationRejected   ObservationStatus = "rejected"
)

var (
	ErrControlInvalid             = errors.New("qualification Receipt v3 durable control input is invalid")
	ErrControlNotFound            = errors.New("qualification Receipt v3 durable control record is not found")
	ErrControlConflict            = errors.New("qualification Receipt v3 durable control conflicts with immutable state")
	ErrControlOutcomeUnknown      = errors.New("qualification Receipt v3 durable control outcome is unknown; inspect the exact record")
	ErrControlStoreOutcomeUnknown = errors.New("qualification Receipt v3 durable control store commit outcome is unknown")
	ErrControlNotReady            = errors.New("qualification Receipt v3 durable control is not ready for completion")
)

type RequestKind string
type ControlRole string
type ObservationStatus string

// StartCommand is opaque. Request bodies, generations, evidence projections,
// and payload bytes are never accepted from a browser-facing caller.
type StartCommand struct {
	AuthorityID uuid.UUID
	OperationID uuid.UUID
}

type ControlLookup struct {
	AuthorityID uuid.UUID
	OperationID uuid.UUID
	Kind        RequestKind
}

// ControlRequest is the independently auditable request row. The material is
// role-closed:
//   - snapshot-seal has no SnapshotDigest, ReceiptID, PayloadDigest, or PAE;
//   - snapshot-verify adds SnapshotDigest but still has no Receipt/Payload/PAE;
//   - receipt-sign adds all of them.
//
// The server resolver constructs the exact canonical bytes.
type ControlRequest struct {
	ArtifactIndexDigest    string      `json:"artifactIndexDigest"`
	AuthenticationKeyID    string      `json:"authenticationKeyId"`
	EvidenceClosureDigest  string      `json:"evidenceClosureDigest"`
	EvidenceCommandDigest  string      `json:"evidenceCommandDigest"`
	EvidenceHeadVersion    uint64      `json:"evidenceHeadVersion"`
	EvidenceLastEventHash  string      `json:"evidenceLastEventHash"`
	EvidenceLastEventID    string      `json:"evidenceLastEventId"`
	EvidencePlanHash       string      `json:"evidencePlanHash"`
	EvidenceTrustDigest    string      `json:"evidenceTrustDigest"`
	InputHash              string      `json:"inputHash"`
	Kind                   RequestKind `json:"kind"`
	OperationID            string      `json:"operationId"`
	OrchestrationID        string      `json:"orchestrationId"`
	PAEDigest              string      `json:"paeDigest"`
	PayloadDigest          string      `json:"payloadDigest"`
	PlanAuthorityHash      string      `json:"planAuthorityHash"`
	PlanAuthorityID        string      `json:"planAuthorityId"`
	OperationalAuthorityID string      `json:"operationalAuthorityId"`
	ProjectionHash         string      `json:"projectionHash"`
	ReceiptID              string      `json:"receiptId"`
	Role                   ControlRole `json:"role"`
	SchemaVersion          string      `json:"schemaVersion"`
	SignerIdentity         string      `json:"signerIdentity"`
	SignerKeyID            string      `json:"signerKeyId"`
	SnapshotDigest         string      `json:"snapshotDigest"`
	SnapshotID             string      `json:"snapshotId"`
	TargetHash             string      `json:"targetHash"`
	TrustBindingsDigest    string      `json:"trustBindingsDigest"`
	TrustHash              string      `json:"trustHash"`
	TrustPolicyDigest      string      `json:"trustPolicyDigest"`
}

type ResolvedControlRequest struct {
	Request      ControlRequest
	RequestBytes []byte
	RequestHash  string
	Payload      []byte
	PayloadHash  string
	PAE          []byte
	PAEHash      string
}

type ControlResolution struct {
	Requests []ResolvedControlRequest
}

// PlanEvidenceResolver is installed on the trusted server. It resolves opaque
// Plan Authority/evidence identities into server-built request material. This
// package deliberately has no production or wire-derived implementation.
type PlanEvidenceResolver interface {
	ResolveControl(context.Context, ControlLookup) (ControlResolution, error)
}

type RequestKey struct {
	AuthorityID uuid.UUID
	OperationID uuid.UUID
	Kind        RequestKind
	Role        ControlRole
}

type RequestRecord struct {
	Key RequestKey

	Request      ControlRequest
	RequestBytes []byte
	RequestHash  string
	Payload      []byte
	PayloadHash  string
	PAE          []byte
	PAEHash      string

	StartedAt  time.Time
	Idempotent bool
}

type StartOutcome struct {
	Requests      []RequestRecord
	CallOwnership bool
}

type StoreStartOutcome struct {
	Requests []RequestRecord
	Created  bool
}

type ObservationCommand struct {
	Request                RequestKey
	ObservationAuthorityID uuid.UUID
}

type ObservationLookup struct {
	ObservationAuthorityID uuid.UUID
	RequestHash            string
}

// ObservationAuthenticationPayload is the closed semantic statement signed by
// an operational authority. Its wrapper is retained separately so durable
// storage preserves an independently auditable authentication proof.
type ObservationAuthenticationPayload struct {
	AcknowledgementTokenHash string            `json:"acknowledgementTokenHash"`
	AuthenticationKeyID      string            `json:"authenticationKeyId"`
	OperationalAuthorityID   string            `json:"operationalAuthorityId"`
	PlanAuthorityID          string            `json:"planAuthorityId"`
	ClaimTokenHash           string            `json:"claimTokenHash"`
	Generation               uint64            `json:"generation"`
	Kind                     RequestKind       `json:"kind"`
	ObservedAt               string            `json:"observedAt"`
	OperationID              string            `json:"operationId"`
	RequestHash              string            `json:"requestHash"`
	ResultHash               string            `json:"resultHash"`
	Role                     ControlRole       `json:"role"`
	SchemaVersion            string            `json:"schemaVersion"`
	Sequence                 uint64            `json:"sequence"`
	SignatureHash            string            `json:"signatureHash"`
	Status                   ObservationStatus `json:"status"`
}

type ObservationAuthenticationEnvelope struct {
	Algorithm              string `json:"algorithm"`
	KeyID                  string `json:"keyId"`
	OperationalAuthorityID string `json:"operationalAuthorityId"`
	Payload                string `json:"payload"`
	PayloadType            string `json:"payloadType"`
	SchemaVersion          string `json:"schemaVersion"`
	Signature              string `json:"signature"`
}

type ClaimToken struct {
	OperationalAuthorityID string      `json:"operationalAuthorityId"`
	PlanAuthorityID        string      `json:"planAuthorityId"`
	ClaimID                string      `json:"claimId"`
	Generation             uint64      `json:"generation"`
	Kind                   RequestKind `json:"kind"`
	OperationID            string      `json:"operationId"`
	PendingEnvelopeHash    string      `json:"pendingEnvelopeHash"`
	RequestHash            string      `json:"requestHash"`
	Role                   ControlRole `json:"role"`
	SchemaVersion          string      `json:"schemaVersion"`
}

type AcknowledgementToken struct {
	AcknowledgementID string            `json:"acknowledgementId"`
	ClaimTokenHash    string            `json:"claimTokenHash"`
	RequestHash       string            `json:"requestHash"`
	SchemaVersion     string            `json:"schemaVersion"`
	Status            ObservationStatus `json:"status"`
}

type ResolvedObservation struct {
	Generation uint64
	Sequence   uint64
	Status     ObservationStatus
	ObservedAt time.Time

	AuthenticationKeyID        string
	AuthenticationPayload      ObservationAuthenticationPayload
	AuthenticationPayloadBytes []byte
	AuthenticationPayloadHash  string
	AuthenticationEnvelope     ObservationAuthenticationEnvelope
	AuthenticationBytes        []byte
	AuthenticationEnvelopeHash string

	// Result is the strict decoded canonical JSON representation retained
	// independently from its exact raw bytes. It is required only for committed
	// sealer/verifier observations.
	Result      json.RawMessage
	ResultBytes []byte
	ResultHash  string

	// Signature is the exact raw Ed25519 signature. It is required only for a
	// committed receipt-sign observation.
	Signature     []byte
	SignatureHash string

	Claim           ClaimToken
	ClaimBytes      []byte
	ClaimTokenHash  string
	Acknowledgement AcknowledgementToken
	AckBytes        []byte
	AckTokenHash    string
}

type AuthenticatedObservationResolver interface {
	ResolveObservation(context.Context, ObservationLookup) (ResolvedObservation, error)
}

type ObservationRecord struct {
	RequestKey  RequestKey
	RequestHash string

	Generation uint64
	Sequence   uint64
	Status     ObservationStatus
	ObservedAt time.Time
	RecordedAt time.Time

	AuthenticationKeyID        string
	AuthenticationPayload      ObservationAuthenticationPayload
	AuthenticationPayloadBytes []byte
	AuthenticationPayloadHash  string
	AuthenticationEnvelope     ObservationAuthenticationEnvelope
	AuthenticationBytes        []byte
	AuthenticationEnvelopeHash string

	Result      json.RawMessage
	ResultBytes []byte
	ResultHash  string

	Signature     []byte
	SignatureHash string

	Claim           ClaimToken
	ClaimBytes      []byte
	ClaimTokenHash  string
	Acknowledgement AcknowledgementToken
	AckBytes        []byte
	AckTokenHash    string

	RecordHash string
	Idempotent bool
}

type RetryOutcome struct {
	Observation   ObservationRecord
	CallOwnership bool
}

type CompletionCommand struct {
	AuthorityID            uuid.UUID
	SnapshotOperationID    uuid.UUID
	ReceiptSignOperationID uuid.UUID
}

type CompletionRequestHashes struct {
	ApproverSign   string `json:"approverSign"`
	RunnerSign     string `json:"runnerSign"`
	SnapshotSeal   string `json:"snapshotSeal"`
	SnapshotVerify string `json:"snapshotVerify"`
}

type CompletionObservationHashes struct {
	ApproverSign   string `json:"approverSign"`
	RunnerSign     string `json:"runnerSign"`
	SnapshotSeal   string `json:"snapshotSeal"`
	SnapshotVerify string `json:"snapshotVerify"`
}

type CompletionOperations struct {
	ReceiptSign string `json:"receiptSign"`
	Snapshot    string `json:"snapshot"`
}

type CompletionDocument struct {
	PlanAuthorityID       string                      `json:"planAuthorityId"`
	CompletedAt           string                      `json:"completedAt"`
	EnvelopeHash          string                      `json:"envelopeHash"`
	EvidenceClosureDigest string                      `json:"evidenceClosureDigest"`
	ObservationHashes     CompletionObservationHashes `json:"observationHashes"`
	Operations            CompletionOperations        `json:"operations"`
	PAEDigest             string                      `json:"paeDigest"`
	PayloadDigest         string                      `json:"payloadDigest"`
	PlanAuthorityHash     string                      `json:"planAuthorityHash"`
	ReceiptID             string                      `json:"receiptId"`
	RequestHashes         CompletionRequestHashes     `json:"requestHashes"`
	SchemaVersion         string                      `json:"schemaVersion"`
	SnapshotDigest        string                      `json:"snapshotDigest"`
	SnapshotID            string                      `json:"snapshotId"`
}

type CompletionRecord struct {
	AuthorityID uuid.UUID
	ReceiptID   string

	PlanAuthorityHash     string
	EvidenceClosureDigest string
	SnapshotID            string
	SnapshotDigest        string

	RequestHashes     CompletionRequestHashes
	ObservationHashes CompletionObservationHashes
	Operations        CompletionOperations

	Payload        []byte
	PayloadDigest  string
	PAE            []byte
	PAEDigest      string
	Envelope       []byte
	EnvelopeDigest string

	Document      CompletionDocument
	DocumentBytes []byte
	DocumentHash  string
	CompletedAt   time.Time
	Idempotent    bool

	// verificationEnvelopeHash is an unexported in-process grant assigned only
	// after ControlService's keyful Verifier and ExpectedResolver accept the
	// reconstructed envelope. It prevents an external package from bypassing
	// verification by calling an exported Store implementation directly.
	verificationEnvelopeHash string
}

// ControlStore is the append-only durable-control contract. StartBatch must
// assign StartedAt with a trusted clock and atomically freeze both signing
// rows. Observations are keyed by (requestHash, sequence), and Complete must
// atomically recheck the four terminal observations.
type ControlStore interface {
	StartBatch(context.Context, []RequestRecord) (StoreStartOutcome, error)
	InspectAttempt(context.Context, ControlLookup) ([]RequestRecord, error)
	InspectRequest(context.Context, RequestKey) (RequestRecord, error)
	AppendObservation(context.Context, ObservationRecord) (ObservationRecord, error)
	InspectObservation(context.Context, string, uint64) (ObservationRecord, error)
	InspectTerminalObservation(context.Context, string) (ObservationRecord, error)
	Complete(context.Context, CompletionRecord) (CompletionRecord, error)
	InspectCompletion(context.Context, uuid.UUID) (CompletionRecord, error)
}
