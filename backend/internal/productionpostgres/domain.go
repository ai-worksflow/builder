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

const ResultSchemaVersion = "worksflow-production-postgresql-posture-result/v1"

type RoleKind string

const (
	RoleApplication   RoleKind = "application"
	RoleMigrator      RoleKind = "migrator"
	RoleQualification RoleKind = "qualification"
	RolePromotion     RoleKind = "promotion"
)

const (
	StatusPassed     = "passed"
	StatusFailed     = "failed"
	StatusNotChecked = "not-checked"
)

const (
	FailureConfigurationInvalid     = "configuration_invalid"
	FailureConnectionUnavailable    = "connection_unavailable"
	FailureCatalogInspectionFailed  = "catalog_inspection_failed"
	FailureApplicationPostureUnsafe = "application_posture_unsafe"
	FailureMigratorPostureUnsafe    = "migrator_posture_unsafe"
	FailureAuditorPostureUnsafe     = "qualification_posture_unsafe"
	FailurePromotionPostureUnsafe   = "promotion_posture_unsafe"
	FailureIdentityScopeMismatch    = "identity_scope_mismatch"
)

var (
	ErrInvalidConfiguration = errors.New("invalid production PostgreSQL posture configuration")
	ErrUnsafePosture        = errors.New("unsafe production PostgreSQL role posture")
	ErrOperational          = errors.New("production PostgreSQL posture check could not complete")
)

// Config contains credential material only in memory. Callers must never log
// or serialize it. The standalone command loads each value from a separate,
// permission-checked file rather than accepting secrets as arguments or
// direct environment values.
type Config struct {
	ApplicationDSN   string `json:"-"`
	MigratorDSN      string `json:"-"`
	QualificationDSN string `json:"-"`
	PromotionDSN     string `json:"-"`
	Schema           string `json:"-"`
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
	SchemaVersion  string       `json:"schemaVersion"`
	Status         string       `json:"status"`
	CheckedAt      string       `json:"checkedAt"`
	Schema         string       `json:"schema,omitempty"`
	EvidenceClass  string       `json:"evidenceClass"`
	Roles          []RoleResult `json:"roles"`
	ExcludedClaims []string     `json:"excludedClaims"`
	Failure        *Failure     `json:"failure,omitempty"`
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
				Responsibility: "dedicated qualification-promotion consume and pending-handoff reader",
				Status:         StatusNotChecked,
			},
		},
		ExcludedClaims: []string{
			"external-qualification-receipt",
			"gc-scheduler-qualification",
			"promotion-authority",
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
	case RoleApplication, RoleMigrator, RoleQualification, RolePromotion:
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
