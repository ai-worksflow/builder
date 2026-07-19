// Package qualificationpromotion atomically consumes the exact output of the
// external qualification verifier and creates an immutable downstream handoff.
//
// This package is deliberately not an HTTP boundary. Consume never accepts a
// client-supplied VerifiedPromotion projection: the server-configured verifier
// is invoked inside Service before the append-only ledger is touched.
package qualificationpromotion

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

const (
	RequestSchemaV1        = "worksflow-qualification-promotion-consume-request/v1"
	RevisionIntentSchemaV1 = "worksflow-qualification-promotion-revision-intent/v1"
	RevisionIntentKindV1   = "external-qualification-promotion-only/v1"
	HandoffStatePending    = "pending"
)

var (
	ErrInvalid          = errors.New("invalid qualification promotion consumption")
	ErrConflict         = errors.New("qualification promotion consumption conflict")
	ErrNotFound         = errors.New("qualification promotion consumption not found")
	ErrOutcomeUnknown   = errors.New("qualification promotion commit outcome is unknown; inspect the same operation")
	ErrAuthorityExpired = errors.New("qualification promotion authority expired before atomic consumption")
)

// Verifier is installed by the trusted promotion operator. Its concrete
// qualificationreceipt.Verifier reads and verifies the immutable evidence
// snapshot. It is never selected by a request or supplied by a browser.
type Verifier interface {
	Verify(receiptPath, indexPath, artifactRoot string, expected qualificationreceipt.ExpectedPromotion) (qualificationreceipt.VerifiedPromotion, error)
}

// VerificationInput contains only immutable snapshot locations and the
// server-owned expectation. None of these values may be populated from a
// client projection of verifier output.
type VerificationInput struct {
	ReceiptPath  string
	IndexPath    string
	ArtifactRoot string
	Expected     qualificationreceipt.ExpectedPromotion
}

// AuthorityResolution is returned only by the server-configured expectation
// authority. A browser-facing command carries only the opaque reference ID.
type AuthorityResolution struct {
	Target       qualificationreceipt.PromotionTarget
	Verification VerificationInput
}

// ExpectationAuthority resolves immutable evidence paths, the root-owned
// ExpectedPromotion, and the current exact workflow target from trusted
// server storage. It is installed at process startup, never per request.
type ExpectationAuthority interface {
	Resolve(context.Context, uuid.UUID) (AuthorityResolution, error)
}

// ConsumeCommand preallocates both identities needed to make a retry after an
// uncertain commit unambiguous. Target is loaded from current workflow state
// by the operator and must exactly equal both Expected and verifier output.
type ConsumeCommand struct {
	OperationID              uuid.UUID
	QualificationAuthorityID uuid.UUID
	HandoffID                uuid.UUID
	OutputRevisionID         uuid.UUID
}

// ConsumeRequest is the canonical, hash-bound idempotency document stored as
// both exact bytes and a parsed JSON document.
type ConsumeRequest struct {
	HandoffID                string `json:"handoffId"`
	OperationID              string `json:"operationId"`
	OutputRevisionID         string `json:"outputRevisionId"`
	QualificationAuthorityID string `json:"qualificationAuthorityId"`
	RevisionIntentDigest     string `json:"revisionIntentDigest"`
	SchemaVersion            string `json:"schemaVersion"`
	TargetDigest             string `json:"targetDigest"`
	VerifiedPromotionHash    string `json:"verifiedPromotionHash"`
}

// RevisionIntent is immutable and sufficient for a dedicated workflow
// operator to reconstruct the exact promotion-only revision it must create.
// This package does not claim that final workflow mutation has happened.
type RevisionIntent struct {
	AuthorityNonce           string                               `json:"authorityNonce"`
	HandoffID                string                               `json:"handoffId"`
	OutputRevisionID         string                               `json:"outputRevisionId"`
	PromotionAuthorityDigest string                               `json:"promotionAuthorityDigest"`
	RevisionKind             string                               `json:"revisionKind"`
	SchemaVersion            string                               `json:"schemaVersion"`
	SourceTarget             qualificationreceipt.PromotionTarget `json:"sourceTarget"`
	VerifiedPromotionHash    string                               `json:"verifiedPromotionHash"`
}

type HandoffRecord struct {
	HandoffID                uuid.UUID
	OperationID              uuid.UUID
	State                    string
	Target                   qualificationreceipt.PromotionTarget
	OutputRevisionID         uuid.UUID
	RevisionKind             string
	RevisionIntentDigest     string
	RevisionIntentBytes      []byte
	RevisionIntent           RevisionIntent
	AuthorityNonce           string
	PromotionAuthorityDigest string
	VerifiedPromotionHash    string
	CreatedAt                time.Time
}

type ConsumptionRecord struct {
	OperationID              uuid.UUID
	QualificationAuthorityID uuid.UUID
	RequestHash              string
	RequestBytes             []byte
	Request                  ConsumeRequest
	TargetDigest             string
	VerifiedPromotionHash    string
	VerifiedPromotionBytes   []byte
	VerifiedPromotion        qualificationreceipt.VerifiedPromotion
	ConsumedAt               time.Time
	Handoff                  HandoffRecord
	Idempotent               bool
}

// ConsumptionKey is the exact authority capability consumed by the ledger.
// The nonce is also globally unique, so reusing it for another target or
// authority digest is always a conflict rather than a second capability.
type ConsumptionKey struct {
	Target                   qualificationreceipt.PromotionTarget
	AuthorityNonce           string
	PromotionAuthorityDigest string
}

type appendCommand struct {
	record ConsumptionRecord
}

// store is intentionally package-private: untrusted application packages
// cannot substitute a persistence implementation that weakens atomicity.
type store interface {
	trustedTime(context.Context) (time.Time, error)
	append(context.Context, appendCommand) (ConsumptionRecord, error)
	inspectOperation(context.Context, uuid.UUID) (ConsumptionRecord, error)
	inspectKey(context.Context, ConsumptionKey) (ConsumptionRecord, error)
}
