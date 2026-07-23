// Package productionpostgres verifies a bounded-window PostgreSQL identity and
// privilege posture used by a production Worksflow deployment. Each identity
// is inspected on its own connection and therefore its own catalog snapshot.
//
// It is deliberately independent from qualification receipts and promotion.
// A passing result does not qualify the repository-index GC scheduler or any
// external deployment workflow.
package productionpostgres

import (
	"errors"
	"time"
)

const ResultSchemaVersion = "worksflow-production-postgresql-posture-result/v2"

type PromotionSessionAffinity string

const (
	PromotionSessionAffinityDirect      PromotionSessionAffinity = "direct"
	PromotionSessionAffinitySessionPool PromotionSessionAffinity = "session-pool"
)

const PromotionRuntimeGateDisabledPendingInputPrecommitAuthorityCanary = "disabled-pending-input-precommit-authority-canary"

type RoleKind string

const (
	RoleApplication        RoleKind = "application"
	RoleMigrator           RoleKind = "migrator"
	RoleQualification      RoleKind = "qualification"
	RolePromotion          RoleKind = "promotion"
	RolePolicy             RoleKind = "qualification-policy"
	RoleInputPrecommit     RoleKind = "qualification-input-precommit"
	RoleSourceVerifier     RoleKind = "qualification-source-verifier"
	RoleCredentialResolver RoleKind = "qualification-credential-resolver"
	RoleHandoff            RoleKind = "qualification-handoff"
)

const (
	StatusPassed     = "passed"
	StatusFailed     = "failed"
	StatusNotChecked = "not-checked"
)

const (
	FailureConfigurationInvalid            = "configuration_invalid"
	FailureConnectionUnavailable           = "connection_unavailable"
	FailureCatalogInspectionFailed         = "catalog_inspection_failed"
	FailureApplicationPostureUnsafe        = "application_posture_unsafe"
	FailureMigratorPostureUnsafe           = "migrator_posture_unsafe"
	FailureAuditorPostureUnsafe            = "qualification_posture_unsafe"
	FailurePromotionPostureUnsafe          = "promotion_posture_unsafe"
	FailurePolicyPostureUnsafe             = "qualification_policy_posture_unsafe"
	FailureInputPrecommitPostureUnsafe     = "qualification_input_precommit_posture_unsafe"
	FailureSourceVerifierPostureUnsafe     = "qualification_source_verifier_posture_unsafe"
	FailureCredentialResolverPostureUnsafe = "qualification_credential_resolver_posture_unsafe"
	FailureHandoffPostureUnsafe            = "qualification_handoff_posture_unsafe"
	FailureIdentityScopeMismatch           = "identity_scope_mismatch"
)

var (
	ErrInvalidConfiguration = errors.New("invalid production PostgreSQL posture configuration")
	ErrUnsafePosture        = errors.New("unsafe production PostgreSQL role posture")
	ErrOperational          = errors.New("production PostgreSQL posture check could not complete")
)

// Config keeps credential material only in memory and carries fail-closed,
// non-secret Promotion/Input session-affinity and runtime-gate declarations.
// Callers must never log or serialize the Config. The standalone command loads
// each DSN from a separate, permission-checked file rather than accepting
// secrets as arguments or direct environment values.
type Config struct {
	ApplicationDSN                    string                   `json:"-"`
	MigratorDSN                       string                   `json:"-"`
	QualificationDSN                  string                   `json:"-"`
	PromotionDSN                      string                   `json:"-"`
	PromotionSessionAffinity          PromotionSessionAffinity `json:"-"`
	PromotionRuntimeGate              string                   `json:"-"`
	PolicyDSN                         string                   `json:"-"`
	InputPrecommitDSN                 string                   `json:"-"`
	InputPrecommitSessionAffinity     PromotionSessionAffinity `json:"-"`
	SourceVerifierDSN                 string                   `json:"-"`
	SourceVerifierSessionAffinity     PromotionSessionAffinity `json:"-"`
	CredentialResolverDSN             string                   `json:"-"`
	CredentialResolverSessionAffinity PromotionSessionAffinity `json:"-"`
	HandoffDSN                        string                   `json:"-"`
	HandoffSessionAffinity            PromotionSessionAffinity `json:"-"`
	Schema                            string                   `json:"-"`
}

type RoleResult struct {
	Kind           RoleKind `json:"kind"`
	Identity       string   `json:"identity,omitempty"`
	Responsibility string   `json:"responsibility"`
	Status         string   `json:"status"`
}

type Failure struct {
	Code string   `json:"code"`
	Role RoleKind `json:"role,omitempty"`
}

// Result is a safe, structured projection. It contains no DSN, endpoint,
// password, credential-file path, or database driver error.
type Result struct {
	SchemaVersion                     string                   `json:"schemaVersion"`
	Status                            string                   `json:"status"`
	CheckedAt                         string                   `json:"checkedAt"`
	Schema                            string                   `json:"schema,omitempty"`
	EvidenceClass                     string                   `json:"evidenceClass"`
	PromotionSessionAffinity          PromotionSessionAffinity `json:"promotionSessionAffinity,omitempty"`
	PromotionRuntimeGate              string                   `json:"promotionRuntimeGate,omitempty"`
	InputPrecommitSessionAffinity     PromotionSessionAffinity `json:"inputPrecommitSessionAffinity,omitempty"`
	SourceVerifierSessionAffinity     PromotionSessionAffinity `json:"sourceVerifierSessionAffinity,omitempty"`
	CredentialResolverSessionAffinity PromotionSessionAffinity `json:"credentialResolverSessionAffinity,omitempty"`
	HandoffSessionAffinity            PromotionSessionAffinity `json:"handoffSessionAffinity,omitempty"`
	Roles                             []RoleResult             `json:"roles"`
	ExcludedClaims                    []string                 `json:"excludedClaims"`
	Failure                           *Failure                 `json:"failure,omitempty"`
}

func newResult(now time.Time, schema string) Result {
	return Result{
		SchemaVersion: ResultSchemaVersion,
		Status:        StatusFailed,
		CheckedAt:     now.UTC().Format(time.RFC3339Nano),
		Schema:        schema,
		EvidenceClass: "standalone-point-in-time-posture-check",
		Roles: []RoleResult{
			{
				Kind:           RoleApplication,
				Responsibility: "API runtime least-privilege data plane",
				Status:         StatusNotChecked,
			},
			{
				Kind:           RoleMigrator,
				Responsibility: "one-shot migration-owner schema authority",
				Status:         StatusNotChecked,
			},
			{
				Kind:           RoleQualification,
				Responsibility: "read-only catalog auditor with no trusted-schema data or function access",
				Status:         StatusNotChecked,
			},
			{
				Kind:           RolePromotion,
				Responsibility: "disabled Promotion-v2 consumer with no handoff-resolver or data-plane table authority",
				Status:         StatusNotChecked,
			},
			{
				Kind:           RolePolicy,
				Responsibility: "dedicated qualification-policy issuer and resolver",
				Status:         StatusNotChecked,
			},
			{
				Kind:           RoleInputPrecommit,
				Responsibility: "disabled qualification input-precommit authority issuer and resolver",
				Status:         StatusNotChecked,
			},
			{
				Kind:           RoleSourceVerifier,
				Responsibility: "disabled qualification source-verifier receipt admission",
				Status:         StatusNotChecked,
			},
			{
				Kind:           RoleCredentialResolver,
				Responsibility: "disabled qualification credential-resolver receipt admission",
				Status:         StatusNotChecked,
			},
			{
				Kind:           RoleHandoff,
				Responsibility: "disabled private qualification handoff completion and inspection",
				Status:         StatusNotChecked,
			},
		},
		ExcludedClaims: []string{
			"external-qualification-receipt",
			"gc-scheduler-qualification",
			"promotion-authority",
			"promotion-runtime-activation",
			"input-precommit-authority-canary",
		},
	}
}

type checkFailure struct {
	sentinel error
	code     string
	role     RoleKind
}

func (e *checkFailure) Error() string {
	switch e.role {
	case RoleApplication, RoleMigrator, RoleQualification, RolePromotion, RolePolicy,
		RoleInputPrecommit, RoleSourceVerifier, RoleCredentialResolver, RoleHandoff:
		return string(e.role) + " production PostgreSQL posture check failed"
	default:
		return "production PostgreSQL posture check failed"
	}
}

func (e *checkFailure) Unwrap() error { return e.sentinel }

func fail(result *Result, sentinel error, code string, role RoleKind) error {
	result.Status = StatusFailed
	result.Failure = &Failure{Code: code, Role: role}
	for index := range result.Roles {
		if result.Roles[index].Kind == role {
			result.Roles[index].Status = StatusFailed
		}
	}
	return &checkFailure{sentinel: sentinel, code: code, role: role}
}

// FailureDetails returns only stable, non-secret classification fields.
func FailureDetails(err error) (string, RoleKind) {
	var failure *checkFailure
	if errors.As(err, &failure) {
		return failure.code, failure.role
	}
	return FailureCatalogInspectionFailed, ""
}
