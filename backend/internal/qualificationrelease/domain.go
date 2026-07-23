package qualificationrelease

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	WorkflowExecutionProfileVersion = "workflow-engine/v3"
	WorkflowExecutionProfileHash    = "854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104"

	ControllerIdentitySchemaVersion = "release-delivery-controller-identity/v1"
	ControllerProtocolV3            = "worksflow.release-delivery/v3"
)

var (
	ErrInvalid             = errors.New("qualification release request is invalid")
	ErrNotFound            = errors.New("qualification release authority was not found")
	ErrNotReady            = errors.New("qualification release is not ready")
	ErrConflict            = errors.New("qualification release authority conflicts")
	ErrRetryable           = errors.New("qualification release transaction can be retried")
	ErrOutcomeUnknown      = errors.New("qualification release outcome is unknown")
	ErrStoreOutcomeUnknown = errors.New("qualification release store commit outcome is unknown")
	ErrLeaseLost           = errors.New("qualification release workflow lease was lost")
)

var exactHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Target struct {
	WorkflowRunID    uuid.UUID
	PublishNodeRunID uuid.UUID
}

func (target Target) Validate() error {
	if !validUUID(target.WorkflowRunID) || !validUUID(target.PublishNodeRunID) ||
		target.WorkflowRunID == target.PublishNodeRunID {
		return ErrInvalid
	}
	return nil
}

type ControllerIdentity struct {
	SchemaVersion  string
	ID             string
	Version        string
	Protocol       string
	TrustKeyDigest string
}

func (identity ControllerIdentity) Validate() error {
	if identity.SchemaVersion != ControllerIdentitySchemaVersion ||
		!boundedText(identity.ID, 200) || !boundedText(identity.Version, 120) ||
		identity.Protocol != ControllerProtocolV3 ||
		!exactHashPattern.MatchString(identity.TrustKeyDigest) {
		return ErrInvalid
	}
	return nil
}

type Authorization struct {
	AuthorizationID         uuid.UUID
	OperationID             uuid.UUID
	ProjectID               uuid.UUID
	WorkflowRunID           uuid.UUID
	PublishNodeRunID        uuid.UUID
	ExpectedProductionRunID uuid.UUID
	AuthorizationHash       string
	AuthorizationDocument   json.RawMessage
}

func (authorization Authorization) ValidateFor(target Target) error {
	if target.Validate() != nil || !validUUID(authorization.AuthorizationID) ||
		!validUUID(authorization.OperationID) || !validUUID(authorization.ProjectID) ||
		!validUUID(authorization.ExpectedProductionRunID) ||
		authorization.WorkflowRunID != target.WorkflowRunID ||
		authorization.PublishNodeRunID != target.PublishNodeRunID ||
		!exactHashPattern.MatchString(authorization.AuthorizationHash) ||
		len(authorization.AuthorizationDocument) == 0 {
		return ErrConflict
	}
	return nil
}

type Claim struct {
	ClaimEventID    uuid.UUID
	AuthorizationID uuid.UUID
	Attempt         int
	Owner           string
	LeaseExpiresAt  time.Time
	Active          bool
	Hash            string
}

func (claim Claim) ValidateFor(authorization Authorization) error {
	if !validUUID(claim.ClaimEventID) || claim.AuthorizationID != authorization.AuthorizationID ||
		claim.Attempt < 1 || !boundedText(claim.Owner, 200) ||
		claim.LeaseExpiresAt.IsZero() || claim.LeaseExpiresAt.Nanosecond()%int(time.Millisecond) != 0 ||
		!exactHashPattern.MatchString(claim.Hash) {
		return ErrConflict
	}
	return nil
}

type ControllerBinding struct {
	AuthorizationID       uuid.UUID
	ProductionRunID       uuid.UUID
	ControllerOperationID uuid.UUID
	ProjectID             uuid.UUID
	RequestHash           string
	Controller            ControllerIdentity
	Hash                  string
}

func (binding ControllerBinding) ValidateFor(authorization Authorization) error {
	if binding.AuthorizationID != authorization.AuthorizationID ||
		binding.ProductionRunID != authorization.ExpectedProductionRunID ||
		binding.ControllerOperationID != binding.ProductionRunID ||
		binding.ProjectID != authorization.ProjectID ||
		!exactHashPattern.MatchString(binding.RequestHash) ||
		!exactHashPattern.MatchString(binding.Hash) || binding.Controller.Validate() != nil {
		return ErrConflict
	}
	return nil
}

type PublishResult struct {
	URL          string `json:"url"`
	DeploymentID string `json:"deploymentId"`
}

func (result PublishResult) Validate() error {
	if !boundedText(result.URL, 2000) {
		return ErrConflict
	}
	if _, err := parseUUID(result.DeploymentID); err != nil {
		return ErrConflict
	}
	return nil
}

type Failure struct {
	SchemaVersion string `json:"schemaVersion"`
	Code          string `json:"code"`
	Outcome       string `json:"outcome"`
}

func (failure Failure) Validate() error {
	if failure.SchemaVersion != "worksflow-qualification-release-workflow-failure/v1" {
		return ErrConflict
	}
	valid := map[string]string{
		"production_failed":    "release_production_checks_failed",
		"controller_rejected":  "release_controller_rejected",
		"pre_submit_cancelled": "release_cancelled_before_submission",
	}
	if valid[failure.Outcome] != failure.Code {
		return ErrConflict
	}
	return nil
}

type OutcomeKind string

const (
	OutcomeActive  OutcomeKind = "active"
	OutcomeHealthy OutcomeKind = "healthy"
	OutcomeFailed  OutcomeKind = "failed"
)

type ControllerOutcome struct {
	Kind        OutcomeKind
	RunState    string
	RemoteState string
	RunVersion  int64
}

func (outcome ControllerOutcome) Validate() error {
	if outcome.RunVersion < 1 {
		return ErrConflict
	}
	switch outcome.Kind {
	case OutcomeActive:
		if !oneOf(outcome.RunState, "queued", "claimed", "submitting", "reconcile_wait", "reconciling", "verifying", "reconcile_blocked") {
			return ErrConflict
		}
	case OutcomeHealthy:
		if outcome.RunState != "healthy" || outcome.RemoteState != "completed" {
			return ErrConflict
		}
	case OutcomeFailed:
		if !oneOf(outcome.RunState, "failed", "error", "cancelled") {
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	return nil
}

type TerminalRecord struct {
	AuthorizationID uuid.UUID
	Outcome         string
	ResultHash      string
	PublishResult   *PublishResult
	Failure         *Failure
	Idempotent      bool
}

func (record TerminalRecord) ValidateFor(authorization Authorization) error {
	if record.AuthorizationID != authorization.AuthorizationID ||
		!exactHashPattern.MatchString(record.ResultHash) {
		return ErrConflict
	}
	if record.Outcome == "healthy" {
		if record.PublishResult == nil || record.Failure != nil || record.PublishResult.Validate() != nil {
			return ErrConflict
		}
		return nil
	}
	if record.PublishResult != nil || record.Failure == nil || record.Failure.Validate() != nil ||
		record.Outcome != record.Failure.Outcome {
		return ErrConflict
	}
	return nil
}

func validUUID(value uuid.UUID) bool {
	return value != uuid.Nil && value.Variant() == uuid.RFC4122
}

func validUUIDv4(value uuid.UUID) bool {
	return validUUID(value) && value.Version() == 4
}

func validUUIDString(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && validUUID(parsed)
}

func boundedText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximum &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func wrap(class error, message string) error {
	return fmt.Errorf("%w: %s", class, message)
}
