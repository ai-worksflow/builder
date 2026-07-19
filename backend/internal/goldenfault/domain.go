// Package goldenfault verifies and consumes the narrowly scoped, one-shot
// fault authorities used by external Golden qualification runs.
//
// The package deliberately contains no shell, URL, SQL, namespace, or signal
// execution surface. A caller can select only one of the audited operation
// kinds below; the corresponding adapter is supplied by trusted service
// configuration.
package goldenfault

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	AuthoritySchemaV1 = "worksflow-golden-fault-authority/v1"
	PayloadTypeV1     = "application/vnd.worksflow.golden-fault-authority+json;version=1"
	ReceiptSchemaV1   = "worksflow-golden-fault-consume-receipt/v1"

	MaximumAuthorityLifetime = 30 * time.Minute
	MaximumClockSkew         = 30 * time.Second
)

var (
	ErrInvalidAuthority = errors.New("invalid Golden fault authority")
	ErrUntrustedSigner  = errors.New("untrusted Golden fault operator signer")
	ErrConflict         = errors.New("Golden fault authority consume conflict")
	ErrOutcomeUnknown   = errors.New("Golden fault authority outcome is unknown; inspect the same authority")
	ErrNotFound         = errors.New("Golden fault authority consume record not found")
	ErrAdapterMissing   = errors.New("Golden fault adapter is not configured")
)

type OperationKind string

const (
	OperationAgentRunnerCrash        OperationKind = "agent-runner-crash"
	OperationAgentRunnerTimeout      OperationKind = "agent-runner-timeout"
	OperationAgentSecurityCanary     OperationKind = "agent-security-canary"
	OperationControllerConflict      OperationKind = "controller-conflict"
	OperationControllerMaintenance   OperationKind = "controller-maintenance"
	OperationControllerNotFound      OperationKind = "controller-not-found"
	OperationControllerTimeout       OperationKind = "controller-timeout"
	OperationLSPResourcePressure     OperationKind = "lsp-resource-pressure"
	OperationLSPRuntimeCrash         OperationKind = "lsp-runtime-crash"
	OperationLSPRuntimeDrift         OperationKind = "lsp-runtime-drift"
	OperationReferenceGatewayOutage  OperationKind = "reference-gateway-outage"
	OperationReferenceProcessRestart OperationKind = "reference-process-restart"
	OperationSandboxDependencyCrash  OperationKind = "sandbox-dependency-crash"
)

func selectorForOperation(operation OperationKind) (string, bool) {
	switch operation {
	case OperationAgentRunnerCrash, OperationAgentRunnerTimeout:
		return "agent.runner", true
	case OperationAgentSecurityCanary:
		return "agent.patch-policy", true
	case OperationControllerConflict, OperationControllerMaintenance, OperationControllerNotFound, OperationControllerTimeout:
		return "release.controller", true
	case OperationLSPResourcePressure, OperationLSPRuntimeCrash, OperationLSPRuntimeDrift:
		return "lsp.runtime", true
	case OperationReferenceGatewayOutage:
		return "reference.gateway", true
	case OperationReferenceProcessRestart:
		return "reference.process", true
	case OperationSandboxDependencyCrash:
		return "sandbox.dependency", true
	default:
		return "", false
	}
}

// AuthorityPredicate is the exact canonical JSON carried directly by the DSSE
// envelope. Field order is lexical so json.Marshal produces the repository's
// canonical wire form.
type AuthorityPredicate struct {
	AuthorityID         string        `json:"authorityId"`
	ExpectedFenceDigest string        `json:"expectedFenceDigest"`
	ExpiresAt           string        `json:"expiresAt"`
	FixtureID           string        `json:"fixtureId"`
	IssuedAt            string        `json:"issuedAt"`
	MaxUses             int           `json:"maxUses"`
	OperationKind       OperationKind `json:"operationKind"`
	ResourceSelector    string        `json:"resourceSelector"`
	RunID               string        `json:"runId"`
	SchemaVersion       string        `json:"schemaVersion"`
}

// ExpectedBinding comes from the already verified Golden fixture. It prevents
// a correctly signed envelope from being substituted across fixtures or runs.
type ExpectedBinding struct {
	AuthorityID         uuid.UUID
	FixtureID           uuid.UUID
	RunID               uuid.UUID
	OperationKind       OperationKind
	ResourceSelector    string
	ExpectedFenceDigest string
	EnvelopeDigest      string
	PayloadDigest       string
}

type VerifiedAuthority struct {
	Predicate        AuthorityPredicate
	EnvelopeDigest   string
	PayloadDigest    string
	SignerIdentities []string
	IssuedAt         time.Time
	ExpiresAt        time.Time
}

type ResourceResolution struct {
	ResourceID  string
	HeadDigest  string
	FenceDigest string
}

type AdapterRequest struct {
	Authority           VerifiedAuthority
	AdapterInvocationID uuid.UUID
	Resource            ResourceResolution
}

type AdapterOutcome string

const (
	AdapterOutcomeApplied AdapterOutcome = "applied"
	AdapterOutcomeRefused AdapterOutcome = "refused"
)

// AdapterResult contains only digest-bound observations. Arbitrary adapter
// output never crosses this boundary and therefore cannot become a command
// channel or an unaudited evidence payload.
type AdapterResult struct {
	Outcome             AdapterOutcome
	ResultDigest        string
	ObservedHeadDigest  string
	ObservedFenceDigest string
}

type Adapter interface {
	// Resolve must be read-only and repeatable. It may discover the dynamic
	// resource/head/fence, but it must not inject or mutate a fault.
	Resolve(context.Context, VerifiedAuthority) (ResourceResolution, error)
	// Execute is called at most once for one durable adapter invocation ID.
	// Implementations must use that ID for their own downstream idempotency.
	Execute(context.Context, AdapterRequest) (AdapterResult, error)
}

type ConsumeState string

const (
	ConsumeStateReserved ConsumeState = "reserved"
	ConsumeStateTerminal ConsumeState = "terminal"
)

type Reservation struct {
	AuthorityID         uuid.UUID
	FixtureID           uuid.UUID
	RunID               uuid.UUID
	OperationKind       OperationKind
	ResourceSelector    string
	ExpectedFenceDigest string
	EnvelopeDigest      string
	PayloadDigest       string
	PredicateDigest     string
	AuthorityIssuedAt   time.Time
	AuthorityExpiresAt  time.Time
	SignerIdentities    []string
	ResolvedResourceID  string
	ResolvedHeadDigest  string
	ResolvedFenceDigest string
	ResolutionDigest    string
	AdapterInvocationID uuid.UUID
	ReservedAt          time.Time
}

type ConsumeReceipt struct {
	AdapterInvocationID uuid.UUID      `json:"adapterInvocationId"`
	AdapterResultDigest string         `json:"adapterResultDigest"`
	AuthorityID         uuid.UUID      `json:"authorityId"`
	CompletedAt         string         `json:"completedAt"`
	EnvelopeDigest      string         `json:"envelopeDigest"`
	ExpectedFenceDigest string         `json:"expectedFenceDigest"`
	FixtureID           uuid.UUID      `json:"fixtureId"`
	ObservedFenceDigest string         `json:"observedFenceDigest"`
	ObservedHeadDigest  string         `json:"observedHeadDigest"`
	OperationKind       OperationKind  `json:"operationKind"`
	Outcome             AdapterOutcome `json:"outcome"`
	PayloadDigest       string         `json:"payloadDigest"`
	PredicateDigest     string         `json:"predicateDigest"`
	ReservedAt          string         `json:"reservedAt"`
	ResolutionDigest    string         `json:"resolutionDigest"`
	ResolvedFenceDigest string         `json:"resolvedFenceDigest"`
	ResolvedHeadDigest  string         `json:"resolvedHeadDigest"`
	ResolvedResourceID  string         `json:"resolvedResourceId"`
	ResourceSelector    string         `json:"resourceSelector"`
	ResultID            uuid.UUID      `json:"resultId"`
	RunID               uuid.UUID      `json:"runId"`
	SchemaVersion       string         `json:"schemaVersion"`
}

type TerminalResult struct {
	AuthorityID         uuid.UUID
	ResultID            uuid.UUID
	Outcome             AdapterOutcome
	AdapterResultDigest string
	ObservedHeadDigest  string
	ObservedFenceDigest string
	CompletedAt         time.Time
	Receipt             ConsumeReceipt
	ReceiptJSON         []byte
	ReceiptDigest       string
}

type ConsumeRecord struct {
	State       ConsumeState
	Reservation Reservation
	Terminal    *TerminalResult
	Idempotent  bool
}

type AuthorityQuery struct {
	AuthorityID    uuid.UUID
	FixtureID      uuid.UUID
	RunID          uuid.UUID
	EnvelopeDigest string
	PayloadDigest  string
}

// Ledger persists the authority reservation before any adapter execution and
// a separate immutable terminal row afterwards. Reserve and CommitTerminal
// must implement insert-CAS semantics and return the existing exact row after
// a response-lost commit.
type Ledger interface {
	TrustedTime(context.Context) (time.Time, error)
	Reserve(context.Context, Reservation) (ConsumeRecord, error)
	CommitTerminal(context.Context, TerminalResult) (ConsumeRecord, error)
	Inspect(context.Context, uuid.UUID) (ConsumeRecord, error)
}
