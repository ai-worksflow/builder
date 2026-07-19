// Package credentialset implements the non-secret control plane for issuing
// and revoking one atomic, short-lived credential set. It deliberately has no
// API for issuing, activating, or revoking an individual member.
package credentialset

import (
	"context"
	"errors"
	"time"
)

const (
	InTotoPayloadType = "application/vnd.in-toto+json"

	IssuanceSchemaV1   = "worksflow-credential-set-issuance/v1"
	RevocationSchemaV1 = "worksflow-credential-set-revocation/v1"
	MemberBindingsV1   = "worksflow-credential-set-member-bindings/v1"

	IssuancePredicateTypeV1   = "https://worksflow.dev/attestations/credential-set-issuance/v1"
	RevocationPredicateTypeV1 = "https://worksflow.dev/attestations/credential-set-revocation/v1"

	MaximumMembers        = 64
	GoldenMemberCount     = 11
	MaximumLifetime       = 30 * time.Minute
	MinimumGoldenLifetime = 2 * time.Minute
	MaximumClockSkew      = 30 * time.Second
)

var (
	ErrInvalid                = errors.New("credential set command or broker result is invalid")
	ErrNotFound               = errors.New("credential set is not found")
	ErrIdempotencyConflict    = errors.New("credential set idempotency key conflicts with prior input")
	ErrCASConflict            = errors.New("credential set state compare-and-swap conflict")
	ErrOutcomeUnknown         = errors.New("credential set operation outcome is unknown; inspect the same operation")
	ErrBrokerRejected         = errors.New("atomic credential broker rejected the operation")
	ErrSignerRejected         = errors.New("credential attestation signer rejected the operation")
	ErrInvalidTransition      = errors.New("credential set state transition is invalid")
	ErrStoreOutcomeUnknown    = errors.New("credential set store commit outcome is unknown")
	ErrDeliveryOutcomeUnknown = errors.New("credential set delivery response outcome is unknown; capability is withheld on replay")
)

type MemberKind string

const (
	MemberKindToken        MemberKind = "token"
	MemberKindStorageState MemberKind = "storage-state"
)

// MemberRequest contains only non-secret identities. The atomic broker owns
// credential generation and returns one-way member handle commitments later.
type MemberRequest struct {
	ActorID string     `json:"actorId"`
	Kind    MemberKind `json:"kind"`
	Slot    string     `json:"slot"`
}

// MemberBinding is safe to persist. CredentialHandleHash is a one-way broker
// commitment and is never a token, cookie, storage state, header, or file path.
type MemberBinding struct {
	ActorID              string     `json:"actorId"`
	CredentialHandleHash string     `json:"credentialHandleHash"`
	Kind                 MemberKind `json:"kind"`
	Slot                 string     `json:"slot"`
}

// SetBinding is the complete non-secret identity of the atomic set. Every
// broker observation, signed predicate, and revocation must repeat it exactly.
type SetBinding struct {
	Audience             string          `json:"audience"`
	ExpiresAt            string          `json:"expiresAt"`
	FixtureID            string          `json:"fixtureId"`
	IssuedAt             string          `json:"issuedAt"`
	Issuer               string          `json:"issuer"`
	MemberBindingsDigest string          `json:"memberBindingsDigest"`
	MemberCount          int             `json:"memberCount"`
	Members              []MemberBinding `json:"members"`
	RunID                string          `json:"runId"`
	SetHandleHash        string          `json:"setHandleHash"`
	SetID                string          `json:"setId"`
}

type IssueCommand struct {
	Audience    string          `json:"audience"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	FixtureID   string          `json:"fixtureId"`
	IssuedAt    time.Time       `json:"issuedAt"`
	Issuer      string          `json:"issuer"`
	Members     []MemberRequest `json:"members"`
	OperationID string          `json:"operationId"`
	RunID       string          `json:"runId"`
	SetID       string          `json:"setId"`
}

// RevokeCommand repeats the exact issued commitment. Callers cannot request a
// partial revocation or substitute a changed member list.
type RevokeCommand struct {
	Binding     SetBinding `json:"binding"`
	OperationID string     `json:"operationId"`
}

// BrokerDeliveryHandle is an intentionally opaque, broker-owned capability.
// Implementations expose their concrete value only to the delivery plane. It
// is passed through by Service and is never copied into Store, Event, Snapshot,
// a signed predicate, or a loggable JSON field.
type BrokerDeliveryHandle interface {
	CredentialSetDeliveryHandle()
	// CredentialSetHandleHash returns the one-way commitment persisted as
	// SetBinding.SetHandleHash. It must not reveal the bearer capability.
	CredentialSetHandleHash() string
}

type BrokerIssueStage string

const (
	BrokerIssuePending  BrokerIssueStage = "pending"
	BrokerIssuePrepared BrokerIssueStage = "prepared"
	BrokerIssueActive   BrokerIssueStage = "active"
	BrokerIssueFailed   BrokerIssueStage = "failed"
)

type BrokerRevokeStage string

const (
	BrokerRevokePending BrokerRevokeStage = "pending"
	BrokerRevokeDone    BrokerRevokeStage = "revoked"
	BrokerRevokeFailed  BrokerRevokeStage = "failed"
)

// BrokerIssueObservation is non-secret. Delivery may be present only for an
// active set and is intentionally excluded from JSON serialization.
type BrokerIssueObservation struct {
	Binding     SetBinding           `json:"binding"`
	Delivery    BrokerDeliveryHandle `json:"-"`
	OperationID string               `json:"operationId"`
	Stage       BrokerIssueStage     `json:"stage"`
}

type BrokerRevokeObservation struct {
	Binding     SetBinding        `json:"binding"`
	OperationID string            `json:"operationId"`
	RevokedAt   string            `json:"revokedAt"`
	Stage       BrokerRevokeStage `json:"stage"`
}

type BrokerPrepareRequest struct {
	Audience    string          `json:"audience"`
	ExpiresAt   string          `json:"expiresAt"`
	FixtureID   string          `json:"fixtureId"`
	IssuedAt    string          `json:"issuedAt"`
	Issuer      string          `json:"issuer"`
	Members     []MemberRequest `json:"members"`
	OperationID string          `json:"operationId"`
	RunID       string          `json:"runId"`
	SetID       string          `json:"setId"`
}

type BrokerOperationRef struct {
	OperationID string `json:"operationId"`
	SetID       string `json:"setId"`
}

type BrokerRevokeRequest struct {
	Binding     SetBinding `json:"binding"`
	OperationID string     `json:"operationId"`
	RevokedAt   string     `json:"revokedAt"`
}

// AtomicBroker is intentionally set-shaped. Implementations must stage every
// requested member under one operation ID, atomically activate the whole set,
// and atomically revoke that same set. There is no member-level fallback.
// Inspect methods are authoritative after an ambiguous outcome; callers must
// never retry the corresponding mutating call.
type AtomicBroker interface {
	PrepareSet(context.Context, BrokerPrepareRequest) (BrokerIssueObservation, error)
	ActivateSet(context.Context, BrokerOperationRef) (BrokerIssueObservation, error)
	InspectIssue(context.Context, BrokerOperationRef) (BrokerIssueObservation, error)
	RevokeSet(context.Context, BrokerRevokeRequest) (BrokerRevokeObservation, error)
	InspectRevocation(context.Context, BrokerOperationRef) (BrokerRevokeObservation, error)
}

type SignRequest struct {
	OperationID   string `json:"operationId"`
	PAE           []byte `json:"pae"`
	PayloadDigest string `json:"payloadDigest"`
	PayloadType   string `json:"payloadType"`
}

type SignObservation struct {
	KeyID       string `json:"keyId"`
	OperationID string `json:"operationId"`
	Signature   []byte `json:"signature"`
}

// Signer retains all private-key and HSM material outside this package. Sign
// is called at most once after a durable signing reservation. If its outcome
// is ambiguous, Service calls Inspect with the same operation ID and never
// signs again.
type Signer interface {
	Sign(context.Context, SignRequest) (SignObservation, error)
	Inspect(context.Context, string) (SignObservation, error)
}

type Attestation struct {
	Envelope       []byte `json:"envelope"`
	EnvelopeDigest string `json:"envelopeDigest"`
	KeyID          string `json:"keyId"`
	Payload        []byte `json:"payload"`
	PayloadDigest  string `json:"payloadDigest"`
}

type IssueResult struct {
	Attestation Attestation `json:"attestation"`
	Binding     SetBinding  `json:"binding"`
	// Delivery is broker-owned and deliberately has no JSON representation.
	// It is present only while the current invocation still owns the capability.
	// A completed-state replay returns nil plus ErrDeliveryOutcomeUnknown; this
	// service does not pretend transport response delivery is exactly-once.
	Delivery BrokerDeliveryHandle `json:"-"`
}

type RevokeResult struct {
	Attestation Attestation `json:"attestation"`
	Binding     SetBinding  `json:"binding"`
	RevokedAt   string      `json:"revokedAt"`
}

type Phase string

const (
	PhaseIssueReserved         Phase = "issue-reserved"
	PhasePrepareStarted        Phase = "prepare-started"
	PhasePrepared              Phase = "prepared"
	PhaseActivationStarted     Phase = "activation-started"
	PhaseActivated             Phase = "activated"
	PhaseIssuanceSignStarted   Phase = "issuance-sign-started"
	PhaseIssued                Phase = "issued"
	PhaseRevocationReserved    Phase = "revocation-reserved"
	PhaseRevocationStarted     Phase = "revocation-started"
	PhaseRevoked               Phase = "revoked"
	PhaseRevocationSignStarted Phase = "revocation-sign-started"
	PhaseComplete              Phase = "complete"
	PhaseIssueFailed           Phase = "issue-failed"
	PhaseRevocationFailed      Phase = "revocation-failed"
)

type EventKind string

const (
	EventIssueReserved         EventKind = "issue-reserved"
	EventPrepareStarted        EventKind = "prepare-started"
	EventPrepared              EventKind = "prepared"
	EventActivationStarted     EventKind = "activation-started"
	EventActivated             EventKind = "activated"
	EventIssuanceSignStarted   EventKind = "issuance-sign-started"
	EventIssued                EventKind = "issued"
	EventRevocationReserved    EventKind = "revocation-reserved"
	EventRevocationStarted     EventKind = "revocation-started"
	EventRevoked               EventKind = "revoked"
	EventRevocationSignStarted EventKind = "revocation-sign-started"
	EventRevocationAttested    EventKind = "revocation-attested"
	EventIssueFailed           EventKind = "issue-failed"
	EventRevocationFailed      EventKind = "revocation-failed"
)

// Event contains commitments and signed public attestations only. There is no
// generic metadata or error-text field into which a secret could be copied.
type Event struct {
	At                    time.Time
	Attestation           *Attestation
	Binding               *SetBinding
	EventID               string
	ExpiresAt             string
	IssueCommandHash      string
	IssuedAt              string
	Kind                  EventKind
	OperationID           string
	RevocationCommandHash string
	RevokedAt             string
}

type Snapshot struct {
	Binding               *SetBinding
	IssueAttestation      *Attestation
	IssueCommandHash      string
	IssueOperationID      string
	ExpiresAt             string
	IssuedAt              string
	LastEventAt           time.Time
	LastEventID           string
	Phase                 Phase
	RevocationAttestation *Attestation
	RevocationCommandHash string
	RevocationOperationID string
	RevokedAt             string
	SetID                 string
	Version               uint64
}

// Store is an append-only event store with a derived current state and CAS.
// CreateIssue must durably reserve a set before any broker mutation. Load must
// be strongly consistent after ErrStoreOutcomeUnknown so Service can reconcile
// the exact EventID. Implementations must return ErrCASConflict only when the
// append definitely did not commit and ErrStoreOutcomeUnknown when the commit
// response is ambiguous.
type Store interface {
	TrustedTime(context.Context) (time.Time, error)
	CreateIssue(context.Context, string, Event) (Snapshot, bool, error)
	Load(context.Context, string) (Snapshot, error)
	Append(context.Context, string, uint64, Event) (Snapshot, error)
	Events(context.Context, string) ([]Event, error)
}

type Clock interface {
	Now() time.Time
}

type MemberValidator func([]MemberRequest) error
