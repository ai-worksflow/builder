// Package qualificationpromotionv2 defines the immutable, version-2
// composition boundary between terminal external-qualification evidence and a
// pending workflow handoff.
//
// Production consumption is intentionally exposed only through AtomicStore.
// A store implementation must resolve, lock, validate, and append all
// authorities in one transaction; this package does not expose a sequence of
// repository reads from which an application could assemble an authority in
// autocommit mode.
package qualificationpromotionv2

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationinputauthority"
)

const (
	ConsumeProtocolV2        = "worksflow-qualification-promotion-consume/v2"
	RequestSchemaV2          = "worksflow-qualification-promotion-consume-request/v2"
	ClosureSchemaV2          = "worksflow-qualification-promotion-closure/v2"
	ConsumptionSchemaV2      = "worksflow-qualification-promotion-consumption/v2"
	HandoffSchemaV2          = "worksflow-qualification-promotion-handoff/v2"
	RevisionIntentSchemaV2   = "worksflow-qualification-promotion-revision-intent/v2"
	EvidenceEventSetSchemaV2 = "worksflow-qualification-promotion-evidence-event-set/v2"
	// AdmissionSchemaV1 reserves reviewed vocabulary only. Migration 81
	// deliberately exposes no admission DTO or registry write API.
	AdmissionSchemaV1   = "worksflow-qualification-promotion-independent-authority-admission/v1"
	StoreBundleSchemaV2 = "worksflow-qualification-promotion-store-bundle/v2"

	HashPrefixV2                 = "worksflow-qualification-promotion-hash/v2"
	RequestHashDomainV2          = "worksflow.qualification-promotion.request/v2"
	ClosureHashDomainV2          = "worksflow.qualification-promotion.closure/v2"
	ConsumptionHashDomainV2      = "worksflow.qualification-promotion.consumption/v2"
	HandoffHashDomainV2          = "worksflow.qualification-promotion.handoff/v2"
	RevisionIntentHashDomainV2   = "worksflow.qualification-promotion.revision-intent/v2"
	EvidenceEventSetHashDomainV2 = "worksflow.qualification-promotion.evidence-event-set/v2"
	// IndependentHashDomainV1 is retained for parity with future verified
	// admissions; initial Promotion records require an explicit empty set.
	IndependentHashDomainV1 = "worksflow.qualification-promotion.independent-authority/v1"

	RevisionKindV2              = "external-qualification-promotion/v2"
	HandoffStatePending         = "pending"
	EvidenceArtifactIndexed     = "artifact-indexed"
	EvidenceIndexCommitted      = "committed"
	ExternalQualificationGate   = "external-qualification"
	ReceiptObservationCommitted = "committed"

	ReceiptRequestSnapshotSeal      = "snapshot-seal"
	ReceiptRequestSnapshotVerify    = "snapshot-verify"
	ReceiptRequestSign              = "receipt-sign"
	ReceiptRoleSealer               = "sealer"
	ReceiptRoleVerifier             = "verifier"
	ReceiptRoleQualificationRunner  = "qualification-runner"
	ReceiptRoleReleaseApprover      = "release-approver"
	SourceTreeDigestSchemaV1        = "worksflow-source-content-tree/v1"
	PlanTargetSchemaV1              = "worksflow-qualification-plan-target/v1"
	PlanTrustSchemaV1               = "worksflow-qualification-plan-trust/v1"
	QualificationPlanArtifactPrefix = "qualification-plan-"

	IndependentModelProfileActivation = "model-profile-activation"
	IndependentProductionPostgreSQL   = "production-postgresql-posture"

	MaximumEvidenceEvents      = 2048
	MaximumCanonicalBytes      = 16 << 20
	MaximumStoreBundleBytes    = 128 << 20
	MaximumJavaScriptSafeInt64 = int64(9007199254740991)
)

var (
	ErrInvalid             = errors.New("qualification promotion v2 is invalid")
	ErrNotFound            = errors.New("qualification promotion v2 record is not found")
	ErrNotReady            = errors.New("qualification promotion v2 prerequisites are not ready")
	ErrStale               = errors.New("qualification promotion v2 authority is stale")
	ErrConflict            = errors.New("qualification promotion v2 conflicts with immutable state")
	ErrRetryable           = errors.New("qualification promotion v2 encountered retryable database contention; retry the same operation")
	ErrStoreOutcomeUnknown = errors.New("qualification promotion v2 store commit outcome is unknown")
	ErrOutcomeUnknown      = errors.New("qualification promotion v2 outcome is unknown; inspect the same operation")
)

// ConsumeCommand is the complete opaque command. All identities are allocated
// by the server and must be canonical, non-zero, pairwise-distinct UUIDv4s.
type ConsumeCommand struct {
	OperationID              uuid.UUID
	WorkflowInputAuthorityID uuid.UUID
	PlanAuthorityID          uuid.UUID
	HandoffID                uuid.UUID
	OutputRevisionID         uuid.UUID
}

// ConsumeRequest is the exact timestamp-free idempotency document.
type ConsumeRequest struct {
	HandoffID                string `json:"handoffId"`
	OperationID              string `json:"operationId"`
	OutputRevisionID         string `json:"outputRevisionId"`
	PlanAuthorityID          string `json:"planAuthorityId"`
	SchemaVersion            string `json:"schemaVersion"`
	WorkflowInputAuthorityID string `json:"workflowInputAuthorityId"`
}

// PromotionTargetV2 is the normalized equality projection used for Workflow
// Input, Plan, Receipt, revision intent, and handoff comparisons. The JSON
// names follow the closed promotion documents in section 4 of the contract.
type PromotionTargetV2 struct {
	TargetArtifactID          string `json:"artifactId"`
	NodeKey                   string `json:"nodeKey"`
	NodeRunID                 string `json:"nodeRunId"`
	ProjectID                 string `json:"projectId"`
	TargetRevisionContentHash string `json:"revisionContentHash"`
	TargetRevisionID          string `json:"revisionId"`
	StageGate                 string `json:"stageGate"`
	Subject                   string `json:"subject"`
	WorkflowRunID             string `json:"workflowRunId"`
}

// WorkflowInputTargetSource and PlanReceiptTargetSource freeze the two
// intentionally different upstream target shapes. nodeRunId and artifactId
// are supplied separately from the locked Workflow Input rows.
type WorkflowInputTargetSource struct {
	ManifestSubject           string
	NodeKey                   string
	ProjectID                 string
	StageGate                 string
	TargetRevisionContentHash string
	TargetRevisionID          string
	WorkflowRunID             string
}

type PlanReceiptTargetRevisionSource struct {
	ContentHash string
	ID          string
}

type PlanReceiptTargetSource struct {
	NodeKey        string
	ProjectID      string
	StageGate      string
	Subject        string
	TargetRevision PlanReceiptTargetRevisionSource
	WorkflowRunID  string
}

type WorkflowInputProjection struct {
	AuthorityHash                    string `json:"authorityHash"`
	AuthorityID                      string `json:"authorityId"`
	InputHash                        string `json:"inputHash"`
	QualificationPolicyAuthorityHash string `json:"qualificationPolicyAuthorityHash"`
	QualificationPolicyAuthorityID   string `json:"qualificationPolicyAuthorityId"`
	TargetHash                       string `json:"targetHash"`
}

// InputPrecommitProjection is the complete, closed Promotion projection of a
// qualification-input-precommit authority. Keep this field set in exact
// parity with qualificationinputauthority.PromotionBinding: Promotion must
// bind the source and credential request/receipt/admission proofs, rather
// than treating the precommit as an opaque authority ID/hash pair.
type InputPrecommitProjection struct {
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

func (projection InputPrecommitProjection) promotionBinding() qualificationinputauthority.PromotionBinding {
	return qualificationinputauthority.PromotionBinding{
		AuthorityHash:                    projection.AuthorityHash,
		AuthorityID:                      projection.AuthorityID,
		CredentialAdmissionHash:          projection.CredentialAdmissionHash,
		CredentialReceiptHash:            projection.CredentialReceiptHash,
		CredentialRequestHash:            projection.CredentialRequestHash,
		Kind:                             projection.Kind,
		QualificationPlanAuthorityHash:   projection.QualificationPlanAuthorityHash,
		QualificationPlanAuthorityID:     projection.QualificationPlanAuthorityID,
		QualificationPolicyAuthorityHash: projection.QualificationPolicyAuthorityHash,
		QualificationPolicyAuthorityID:   projection.QualificationPolicyAuthorityID,
		SourceAdmissionHash:              projection.SourceAdmissionHash,
		SourceReceiptHash:                projection.SourceReceiptHash,
		SourceRequestHash:                projection.SourceRequestHash,
		WorkflowInputAuthorityHash:       projection.WorkflowInputAuthorityHash,
		WorkflowInputAuthorityID:         projection.WorkflowInputAuthorityID,
	}
}

type PlanProjection struct {
	AuthorityHash      string `json:"authorityHash"`
	AuthorityID        string `json:"authorityId"`
	EvidencePlanHash   string `json:"evidencePlanHash"`
	InputAuthorityID   string `json:"inputAuthorityId"`
	InputHash          string `json:"inputHash"`
	OrchestrationID    string `json:"orchestrationId"`
	ProjectionHash     string `json:"projectionHash"`
	QualificationRunID string `json:"qualificationRunId"`
	TargetHash         string `json:"targetHash"`
	TrustHash          string `json:"trustHash"`
}

type EvidenceProjection struct {
	ArtifactIndexDigest   string `json:"artifactIndexDigest"`
	CommandHash           string `json:"commandHash"`
	EventSetDigest        string `json:"eventSetDigest"`
	EvidenceClosureDigest string `json:"evidenceClosureDigest"`
	HeadVersion           int64  `json:"headVersion"`
	LastEventHash         string `json:"lastEventHash"`
	LastEventID           string `json:"lastEventId"`
	Phase                 string `json:"phase"`
	TrustBindingsDigest   string `json:"trustBindingsDigest"`
}

type ReceiptProjection struct {
	ApproverObservationHash     string `json:"approverObservationHash"`
	ApproverRequestHash         string `json:"approverRequestHash"`
	CompletionHash              string `json:"completionHash"`
	EnvelopeHash                string `json:"envelopeHash"`
	PAEHash                     string `json:"paeHash"`
	PayloadHash                 string `json:"payloadHash"`
	ReceiptID                   string `json:"receiptId"`
	RunnerObservationHash       string `json:"runnerObservationHash"`
	RunnerRequestHash           string `json:"runnerRequestHash"`
	SnapshotObservationHash     string `json:"snapshotObservationHash"`
	SnapshotRequestHash         string `json:"snapshotRequestHash"`
	VerificationObservationHash string `json:"verificationObservationHash"`
	VerificationRequestHash     string `json:"verificationRequestHash"`
}

type IndependentAuthorityProjection struct {
	AdmissionRecordHash  string `json:"admissionRecordHash"`
	AuthorityHash        string `json:"authorityHash"`
	AuthorityID          string `json:"authorityId"`
	Kind                 string `json:"kind"`
	ReceiptSchemaVersion string `json:"receiptSchemaVersion"`
	SourceReceiptHash    string `json:"sourceReceiptHash"`
}

type PromotionClosure struct {
	Evidence               EvidenceProjection               `json:"evidence"`
	InputPrecommit         InputPrecommitProjection         `json:"inputPrecommit"`
	IndependentAuthorities []IndependentAuthorityProjection `json:"independentAuthorities"`
	Plan                   PlanProjection                   `json:"plan"`
	Receipt                ReceiptProjection                `json:"receipt"`
	SchemaVersion          string                           `json:"schemaVersion"`
	Target                 PromotionTargetV2                `json:"target"`
	WorkflowInput          WorkflowInputProjection          `json:"workflowInput"`
}

type EvidenceEvent struct {
	EventHash string `json:"eventHash"`
	EventID   string `json:"eventId"`
	Version   int64  `json:"version"`
}

type EvidenceEventSet struct {
	Events          []EvidenceEvent `json:"events"`
	HeadVersion     int64           `json:"headVersion"`
	OrchestrationID string          `json:"orchestrationId"`
	SchemaVersion   string          `json:"schemaVersion"`
}

type AuthorityReference struct {
	AuthorityHash string `json:"authorityHash"`
	AuthorityID   string `json:"authorityId"`
}

type ReceiptReference struct {
	EnvelopeHash string `json:"envelopeHash"`
	ReceiptID    string `json:"receiptId"`
}

type RevisionIntent struct {
	ClosureHash      string             `json:"closureHash"`
	OutputRevisionID string             `json:"outputRevisionId"`
	Plan             AuthorityReference `json:"plan"`
	Receipt          ReceiptReference   `json:"receipt"`
	RequestHash      string             `json:"requestHash"`
	RevisionKind     string             `json:"revisionKind"`
	SchemaVersion    string             `json:"schemaVersion"`
	Target           PromotionTargetV2  `json:"target"`
	WorkflowInput    AuthorityReference `json:"workflowInput"`
}

type Consumption struct {
	ClosureHash        string `json:"closureHash"`
	ConsumedAt         string `json:"consumedAt"`
	OperationID        string `json:"operationId"`
	RequestHash        string `json:"requestHash"`
	RevisionIntentHash string `json:"revisionIntentHash"`
	SchemaVersion      string `json:"schemaVersion"`
}

type Handoff struct {
	ConsumptionHash          string            `json:"consumptionHash"`
	CreatedAt                string            `json:"createdAt"`
	HandoffID                string            `json:"handoffId"`
	OperationID              string            `json:"operationId"`
	OutputRevisionID         string            `json:"outputRevisionId"`
	PlanAuthorityID          string            `json:"planAuthorityId"`
	ReceiptID                string            `json:"receiptId"`
	RevisionIntentHash       string            `json:"revisionIntentHash"`
	SchemaVersion            string            `json:"schemaVersion"`
	State                    string            `json:"state"`
	Target                   PromotionTargetV2 `json:"target"`
	WorkflowInputAuthorityID string            `json:"workflowInputAuthorityId"`
}

// IndependentAuthorityRequirement is the exact current Policy requirement.
// Initial migration-81 semantics require this slice to be non-nil and empty.
type IndependentAuthorityRequirement struct {
	AuthorityHash string
	AuthorityID   string
	Kind          string
}

// TerminalEvidenceEvent is the independently locked final Evidence event.
// Keeping its kind, stage, and artifact-index members separate from the head
// projection prevents a caller from hiding a drifted terminal event behind a
// single aggregate lineage digest.
type TerminalEvidenceEvent struct {
	ArtifactIndexDigest   string
	EvidenceClosureDigest string
	EventHash             string
	EventID               string
	EventKind             string
	Stage                 string
}

// PlanControlBindings are scalar controls retained outside the Promotion wire
// projection but read from the exact locked Plan authority.
type PlanControlBindings struct {
	TrustBindingsDigest string
	TrustPolicyDigest   string
}

// ImmutableContentBinding is one exact upstream artifact identity/content
// pair. Migration 81 compares both fields rather than accepting a synthesized
// lineage digest.
type ImmutableContentBinding struct {
	ContentHash string `json:"contentHash"`
	ID          string `json:"id"`
}

// WorkflowPlanBuildBindings makes the two WIA -> Plan build edges explicit.
type WorkflowPlanBuildBindings struct {
	PlanBuildContract          ImmutableContentBinding
	PlanBuildManifest          ImmutableContentBinding
	WorkflowInputBuildContract ImmutableContentBinding
	WorkflowInputBuildManifest ImmutableContentBinding
}

// SourceBinding is the complete clean source-tree binding carried by a Plan
// input and by its terminal Receipt v3 payload.
type SourceBinding struct {
	Commit           string
	Dirty            bool
	TreeDigest       string
	TreeDigestSchema string
}

type PlanReceiptSourceBindings struct {
	PlanSource    SourceBinding
	ReceiptSource SourceBinding
}

// QualificationPolicyAuthorityBinding is one independently read Policy root.
// PreparedAuthority retains both the locked current row and the identity
// attached to PolicyPlanInputs so neither can be substituted behind a true
// PolicyCurrent bit.
type QualificationPolicyAuthorityBinding struct {
	AuthorityHash string
	AuthorityID   string
}

// ExactDocumentDigestBinding retains independently derived canonical digests
// for one Policy-fixed JSON member and its Plan-input counterpart.
type ExactDocumentDigestBinding struct {
	PlanDigest   string
	PolicyDigest string
}

// CredentialProfilePlanBindings spells out the non-isomorphic Policy
// credentialProfile -> Plan credential mapping. MemberRequestSetDigest is a
// Policy precommit control rather than the runtime memberBindingsDigest, so it
// is validated but deliberately not equated to that different domain.
type CredentialProfilePlanBindings struct {
	PlanAudience                 string
	PlanIssuanceArtifactID       string
	PlanIssuer                   string
	PlanRevocationArtifactID     string
	PolicyAudience               string
	PolicyAuthorityID            string
	PolicyIssuanceArtifactID     string
	PolicyMemberRequestSetDigest string
	PolicyRevocationArtifactID   string
}

// QualificationManifestPlanBindings accounts for the intentionally richer
// Policy profile shape: Policy.PlanDigest maps to the Plan input's separate
// qualificationPlanDigest while the artifact/revision/content fields map to
// Plan.qualificationManifest.
type QualificationManifestPlanBindings struct {
	PlanArtifactID              string
	PlanContentHash             string
	PlanQualificationPlanDigest string
	PlanRevisionID              string
	PolicyArtifactID            string
	PolicyContentHash           string
	PolicyPlanDigest            string
	PolicyRevisionID            string
}

// PolicyPlanInputBindings enumerates every Policy-fixed Plan-input member.
// Each member is compared independently so adding one upstream edge cannot be
// accidentally masked by equality of an unrelated aggregate hash. The Policy
// sourcePolicyDigest is intentionally absent: it is a precommit policy domain,
// not an equality projection stored by WIA or Plan.
type PolicyPlanInputBindings struct {
	ArtifactPolicy        ExactDocumentDigestBinding
	Artifacts             ExactDocumentDigestBinding
	CredentialProfile     CredentialProfilePlanBindings
	GoldenRuntime         ExactDocumentDigestBinding
	OutputPolicy          ExactDocumentDigestBinding
	Outputs               ExactDocumentDigestBinding
	QualificationManifest QualificationManifestPlanBindings
	Recipient             ExactDocumentDigestBinding
	TemplateRelease       ExactDocumentDigestBinding
	TrustBindings         ExactDocumentDigestBinding
	TrustPolicy           ExactDocumentDigestBinding
	PolicyAuthority       QualificationPolicyAuthorityBinding
}

// PlanAuthorityLineageBinding is the normalized per-field projection used to
// compare the locked Plan envelope with predicate.planAuthority. It is not a
// Promotion wire document; both independently decoded sides remain available
// to the semantic reference.
type PlanAuthorityLineageBinding struct {
	ArtifactID          string
	AuthorityHash       string
	AuthorityID         string
	EvidencePlanHash    string
	FreezeOperationID   string
	InputAuthorityID    string
	InputHash           string
	PlanDigest          string
	ProjectionHash      string
	TargetHash          string
	TrustBindingsDigest string
	TrustHash           string
}

type PlanReceiptAuthorityBindings struct {
	Plan    PlanAuthorityLineageBinding
	Receipt PlanAuthorityLineageBinding
}

// PlanReceiptEvidencePlanBindings retains the complete typed evidence Plan
// from the locked Plan row and the independently decoded Receipt predicate.
// qualificationevidence.Plan contains every nested operation, credential,
// artifact, recipient, and output member; no aggregate lineage label stands
// in for equality of those members.
type PlanReceiptEvidencePlanBindings struct {
	Plan    qualificationevidence.Plan
	Receipt qualificationevidence.Plan
}

type PlanReceiptPromotionTarget struct {
	NodeKey        string                    `json:"nodeKey"`
	ProjectID      string                    `json:"projectId"`
	StageGate      string                    `json:"stageGate"`
	Subject        string                    `json:"subject"`
	TargetRevision PlanReceiptTargetRevision `json:"targetRevision"`
	WorkflowRunID  string                    `json:"workflowRunId"`
}

type PlanReceiptTargetRevision struct {
	ContentHash string `json:"contentHash"`
	ID          string `json:"id"`
}

type PlanReceiptTargetDocument struct {
	PromotionTarget PlanReceiptPromotionTarget `json:"promotionTarget"`
	SchemaVersion   string                     `json:"schemaVersion"`
}

type PlanReceiptTargetBindings struct {
	Plan    PlanReceiptTargetDocument
	Receipt PlanReceiptTargetDocument
}

type PlanReceiptTrustDocument struct {
	SchemaVersion     string                              `json:"schemaVersion"`
	TrustBindings     qualificationevidence.TrustBindings `json:"trustBindings"`
	TrustPolicyDigest string                              `json:"trustPolicyDigest"`
}

type PlanReceiptTrustBindings struct {
	Plan    PlanReceiptTrustDocument
	Receipt PlanReceiptTrustDocument
}

type TemplateReleaseLineageBinding struct {
	ApprovalReceiptDigest string `json:"approvalReceiptDigest"`
	ContentHash           string `json:"contentHash"`
	ID                    string `json:"id"`
}

type PlanReceiptTemplateReleaseBindings struct {
	Plan    TemplateReleaseLineageBinding
	Receipt TemplateReleaseLineageBinding
}

type GoldenRuntimeLineageBinding struct {
	AuthorityDocumentArtifactID string `json:"authorityDocumentArtifactId"`
	AuthorityDocumentDigest     string `json:"authorityDocumentDigest"`
	FaultOperationSetDigest     string `json:"faultOperationSetDigest"`
	FixtureDocumentArtifactID   string `json:"fixtureDocumentArtifactId"`
	FixtureDocumentDigest       string `json:"fixtureDocumentDigest"`
	FixtureID                   string `json:"fixtureId"`
}

type PlanReceiptGoldenRuntimeBindings struct {
	Plan    GoldenRuntimeLineageBinding
	Receipt GoldenRuntimeLineageBinding
}

type QualificationManifestLineageBinding struct {
	ArtifactID  string `json:"artifactId"`
	ContentHash string `json:"contentHash"`
	RevisionID  string `json:"revisionId"`
}

type PlanReceiptQualificationManifestBindings struct {
	Plan    QualificationManifestLineageBinding
	Receipt QualificationManifestLineageBinding
}

type BuildLineageBinding struct {
	Contract ImmutableContentBinding `json:"contract"`
	Manifest ImmutableContentBinding `json:"manifest"`
}

type PlanReceiptBuildBindings struct {
	Plan    BuildLineageBinding
	Receipt BuildLineageBinding
}

// TerminalCredentialArtifactBinding retains the full non-secret signed
// artifact commitment present in Receipt v3. Promotion compares ArtifactID to
// the Plan while the remaining fields stay explicit so a fixture cannot hide
// a malformed terminal credential set behind the eight-field projection.
type TerminalCredentialArtifactBinding struct {
	ArtifactID      string
	ContentDigest   string
	PayloadDigest   string
	SignerSetDigest string
}

type TerminalCredentialSetBinding struct {
	Audience             string
	ExpiresAt            string
	Issuance             TerminalCredentialArtifactBinding
	IssuedAt             string
	Issuer               string
	MemberBindingsDigest string
	MemberCount          int
	Revocation           TerminalCredentialArtifactBinding
	RevokedAt            string
	SetHandleHash        string
	SetID                string
}

type PlanReceiptCredentialSetBindings struct {
	Plan    qualificationevidence.CredentialExpectation
	Receipt TerminalCredentialSetBinding
}

// PlanReceiptLineageBindings mirrors the exact Plan-to-Receipt comparisons in
// migration 81. Source remains in PlanReceiptSourceBindings because its clean
// actual-byte-tree semantics predate this richer lineage projection.
type PlanReceiptLineageBindings struct {
	Authority             PlanReceiptAuthorityBindings
	Build                 PlanReceiptBuildBindings
	CredentialSet         PlanReceiptCredentialSetBindings
	EvidencePlan          PlanReceiptEvidencePlanBindings
	GoldenRuntime         PlanReceiptGoldenRuntimeBindings
	QualificationManifest PlanReceiptQualificationManifestBindings
	Target                PlanReceiptTargetBindings
	TemplateRelease       PlanReceiptTemplateReleaseBindings
	Trust                 PlanReceiptTrustBindings
}

// ReceiptRequestBindings are the exact authority/evidence columns retained by
// one immutable Receipt v3 request. Request kind and signer role are included
// because the four controls occupy distinct authority slots even when an
// operation identity is intentionally shared.
type ReceiptRequestBindings struct {
	ArtifactIndexDigest   string
	EvidenceClosureDigest string
	EvidenceCommandDigest string
	EvidenceHeadVersion   int64
	EvidenceLastEventHash string
	EvidenceLastEventID   string
	EvidencePlanHash      string
	EvidenceTrustDigest   string
	InputHash             string
	Kind                  string
	OrchestrationID       string
	PlanAuthorityHash     string
	PlanAuthorityID       string
	ProjectionHash        string
	RequestHash           string
	Role                  string
	TargetHash            string
	TrustBindingsDigest   string
	TrustHash             string
	TrustPolicyDigest     string
}

type ReceiptRequestSet struct {
	ApproverSign   ReceiptRequestBindings
	RunnerSign     ReceiptRequestBindings
	SnapshotSeal   ReceiptRequestBindings
	SnapshotVerify ReceiptRequestBindings
}

type ReceiptObservationBindings struct {
	LatestSequence  int64
	ObservationHash string
	RecordedAt      time.Time
	RequestHash     string
	Sequence        int64
	Status          string
}

type ReceiptObservationSet struct {
	ApproverSign   ReceiptObservationBindings
	RunnerSign     ReceiptObservationBindings
	SnapshotSeal   ReceiptObservationBindings
	SnapshotVerify ReceiptObservationBindings
}

// TerminalReceiptControls make terminality inspectable: the exact four
// requests, their latest committed observations, and the Receipt row's direct
// Plan/Evidence columns all have separate representations.
type TerminalReceiptControls struct {
	ArtifactIndexDigest   string
	CompletedAt           time.Time
	EvidenceClosureDigest string
	OrchestrationID       string
	PlanAuthorityHash     string
	PlanAuthorityID       string
	Requests              ReceiptRequestSet
	Observations          ReceiptObservationSet
}

// PreparedAuthority is accepted only by MemoryStore, the deterministic
// semantic reference used by tests. A PostgreSQL adapter must not construct
// this value through separate reads; it calls the reviewed database function.
type PreparedAuthority struct {
	Evidence                 EvidenceProjection
	EvidenceEventSet         EvidenceEventSet
	EvidenceTerminalEvent    TerminalEvidenceEvent
	IndependentRequirements  []IndependentAuthorityRequirement
	InputPrecommit           InputPrecommitProjection
	Plan                     PlanProjection
	PlanControls             PlanControlBindings
	PlanReceiptLineage       PlanReceiptLineageBindings
	PlanTarget               PromotionTargetV2
	PolicyAuthority          QualificationPolicyAuthorityBinding
	PolicyPlanInputs         PolicyPlanInputBindings
	Receipt                  ReceiptProjection
	ReceiptControls          TerminalReceiptControls
	ReceiptTarget            PromotionTargetV2
	SourceBindings           PlanReceiptSourceBindings
	Target                   PromotionTargetV2
	TargetRevisionArtifactID string
	WorkflowInput            WorkflowInputProjection
	WorkflowPlanBuild        WorkflowPlanBuildBindings

	PolicyCurrent        bool
	TargetCurrent        bool
	WorkflowInputCurrent bool
}

// Record retains every exact document, raw canonical byte sequence, and
// domain hash required to validate a durable aggregate independently.
// Idempotent is response-only metadata and is excluded from equality.
type Record struct {
	Command   ConsumeCommand
	ReceiptID string

	Request      ConsumeRequest
	RequestBytes []byte
	RequestHash  string

	EvidenceEventSet      EvidenceEventSet
	EvidenceEventSetBytes []byte
	EvidenceEventSetHash  string

	Closure      PromotionClosure
	ClosureBytes []byte
	ClosureHash  string

	RevisionIntent      RevisionIntent
	RevisionIntentBytes []byte
	RevisionIntentHash  string

	Consumption      Consumption
	ConsumptionBytes []byte
	ConsumptionHash  string

	Handoff      Handoff
	HandoffBytes []byte
	HandoffHash  string

	ConsumedAt time.Time
	CreatedAt  time.Time
	Idempotent bool
}

// StoreMaterial is one exact canonical document as returned by the reviewed
// PostgreSQL store bundle. BytesHex is the retained byte sequence, not a
// re-serialization of Document.
type StoreMaterial[T any] struct {
	BytesHex string `json:"bytesHex"`
	Document T      `json:"document"`
	Hash     string `json:"hash"`
}

type StoreConsumptionMaterial struct {
	BytesHex   string      `json:"bytesHex"`
	ConsumedAt string      `json:"consumedAt"`
	Document   Consumption `json:"document"`
	Hash       string      `json:"hash"`
}

type StoreHandoffMaterial struct {
	BytesHex         string  `json:"bytesHex"`
	CreatedAt        string  `json:"createdAt"`
	Document         Handoff `json:"document"`
	HandoffID        string  `json:"handoffId"`
	Hash             string  `json:"hash"`
	OutputRevisionID string  `json:"outputRevisionId"`
	State            string  `json:"state"`
}

// StoreBundle is the one closed shape shared by consume and all inspections.
// The consume routine adds a response-only idempotent member outside this
// immutable bundle.
type StoreBundle struct {
	Closure                  StoreMaterial[PromotionClosure] `json:"closure"`
	Consumption              StoreConsumptionMaterial        `json:"consumption"`
	EvidenceEventSet         StoreMaterial[EvidenceEventSet] `json:"evidenceEventSet"`
	Handoff                  StoreHandoffMaterial            `json:"handoff"`
	OperationID              string                          `json:"operationId"`
	PlanAuthorityID          string                          `json:"planAuthorityId"`
	ReceiptID                string                          `json:"receiptId"`
	Request                  StoreMaterial[ConsumeRequest]   `json:"request"`
	RevisionIntent           StoreMaterial[RevisionIntent]   `json:"revisionIntent"`
	SchemaVersion            string                          `json:"schemaVersion"`
	WorkflowInputAuthorityID string                          `json:"workflowInputAuthorityId"`
}

type ConsumeStoreBundle struct {
	Closure                  StoreMaterial[PromotionClosure] `json:"closure"`
	Consumption              StoreConsumptionMaterial        `json:"consumption"`
	EvidenceEventSet         StoreMaterial[EvidenceEventSet] `json:"evidenceEventSet"`
	Handoff                  StoreHandoffMaterial            `json:"handoff"`
	Idempotent               bool                            `json:"idempotent"`
	OperationID              string                          `json:"operationId"`
	PlanAuthorityID          string                          `json:"planAuthorityId"`
	ReceiptID                string                          `json:"receiptId"`
	Request                  StoreMaterial[ConsumeRequest]   `json:"request"`
	RevisionIntent           StoreMaterial[RevisionIntent]   `json:"revisionIntent"`
	SchemaVersion            string                          `json:"schemaVersion"`
	WorkflowInputAuthorityID string                          `json:"workflowInputAuthorityId"`
}

// AtomicStore is the Promotion operator's complete production persistence
// surface. Consume owns the full transaction-bound authority composition and
// InspectOperation is the only reconciliation read granted to that isolated
// role. ErrRetryable is a known-abort result: callers may retry only the exact
// same ConsumeCommand, including all five server-allocated identities.
type AtomicStore interface {
	Consume(context.Context, ConsumeCommand) (Record, error)
	InspectOperation(context.Context, uuid.UUID) (Record, error)
}

// PendingHandoffResolver belongs to the separately isolated workflow handoff
// consumer introduced by migration 82. It must not be folded into AtomicStore:
// the Promotion operator is deliberately denied the handoff resolver and the
// handoff operator is deliberately denied Promotion consume.
type PendingHandoffResolver interface {
	InspectHandoff(context.Context, uuid.UUID) (Record, error)
}
