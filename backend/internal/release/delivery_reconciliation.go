package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

const DeliveryReconciliationCaseSchemaVersion = "release-delivery-reconciliation-case/v1"

var (
	ErrDeliveryReconciliationNotFound = errors.New("release delivery reconciliation case was not found")
	ErrDeliveryReconciliationConflict = errors.New("release delivery reconciliation state changed")
	ErrDeliveryReconciliationLegacy   = errors.New("historical release delivery v1 run cannot be reconciled")
	ErrDeliveryReconciliationDisabled = errors.New("governed release delivery reconciliation is unavailable")
)

// DeliveryReconciliationAttempt freezes the final quarantining Attempt. It is
// evidence of why an operator was needed, not a replacement controller result.
type DeliveryReconciliationAttempt struct {
	Ordinal     uint64              `json:"ordinal"`
	Kind        DeliveryAttemptKind `json:"kind"`
	WorkerID    string              `json:"workerId"`
	FenceEpoch  uint64              `json:"fenceEpoch"`
	StartedAt   time.Time           `json:"startedAt"`
	CompletedAt time.Time           `json:"completedAt"`
	Outcome     string              `json:"outcome"`
	ErrorCode   string              `json:"errorCode"`
	ErrorDetail string              `json:"errorDetail"`
}

type DeliveryReconciliationObservation struct {
	Sequence   uint64    `json:"sequence"`
	ObservedAt time.Time `json:"observedAt"`
}

type DeliveryReconciliationError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// DeliveryReconciliationBlockSnapshot is the side-effect-free CAS input for
// an administrator. It prevents clients from guessing a quarantined error or
// attempting recovery against a stale Run version.
type DeliveryReconciliationBlockSnapshot struct {
	SchemaVersion        string                      `json:"schemaVersion"`
	ProjectID            string                      `json:"projectId"`
	RunKind              DeliveryOperationKind       `json:"runKind"`
	RunID                string                      `json:"runId"`
	RunSchemaVersion     string                      `json:"runSchemaVersion"`
	ExpectedRunVersion   uint64                      `json:"expectedRunVersion"`
	OperationID          string                      `json:"operationId"`
	OperationRequestHash string                      `json:"operationRequestHash"`
	Controller           DeliveryControllerIdentity  `json:"controller"`
	LastError            DeliveryReconciliationError `json:"lastError"`
}

// DeliveryReconciliationCase is immutable operator evidence authorizing one
// and only one edge from reconcile_blocked back to reconcile_wait. It never
// asserts a remote result and never changes the expected production head.
type DeliveryReconciliationCase struct {
	SchemaVersion         string                             `json:"schemaVersion"`
	ID                    string                             `json:"id"`
	ProjectID             string                             `json:"projectId"`
	RunKind               DeliveryOperationKind              `json:"runKind"`
	RunID                 string                             `json:"runId"`
	RunSchemaVersion      string                             `json:"runSchemaVersion"`
	ExpectedRunVersion    uint64                             `json:"expectedRunVersion"`
	OperationID           string                             `json:"operationId"`
	OperationRequestHash  string                             `json:"operationRequestHash"`
	Controller            DeliveryControllerIdentity         `json:"controller"`
	PreviousRemoteState   string                             `json:"previousRemoteState"`
	ResumeRemoteState     string                             `json:"resumeRemoteState"`
	SubmitAttemptCount    uint64                             `json:"submitAttemptCount"`
	ReconcileAttemptCount uint64                             `json:"reconcileAttemptCount"`
	LastAttempt           DeliveryReconciliationAttempt      `json:"lastAttempt"`
	LastObservation       *DeliveryReconciliationObservation `json:"lastObservation"`
	QuarantineError       DeliveryReconciliationError        `json:"quarantineError"`
	ActorID               string                             `json:"actorId"`
	Reason                string                             `json:"reason"`
	IdempotencyKey        string                             `json:"idempotencyKey"`
	RequestHash           string                             `json:"requestHash"`
	CaseHash              string                             `json:"caseHash"`
	CreatedAt             time.Time                          `json:"createdAt"`
}

type newDeliveryReconciliationCaseInput struct {
	Case DeliveryReconciliationCase
}

func newDeliveryReconciliationCase(input newDeliveryReconciliationCaseInput) (DeliveryReconciliationCase, error) {
	value := input.Case
	value.SchemaVersion = DeliveryReconciliationCaseSchemaVersion
	value.PreviousRemoteState = "quarantined"
	value.CaseHash = ""
	value.CreatedAt = value.CreatedAt.UTC().Truncate(time.Microsecond)
	if err := validateDeliveryReconciliationCaseShape(value, false); err != nil {
		return DeliveryReconciliationCase{}, err
	}
	hash, err := domain.CanonicalHash(deliveryReconciliationCaseHashPayload(value))
	if err != nil {
		return DeliveryReconciliationCase{}, invalid("delivery reconciliation Case hash")
	}
	value.CaseHash = "sha256:" + hash
	return value, nil
}

func ParseDeliveryReconciliationCase(value DeliveryReconciliationCase) (DeliveryReconciliationCase, error) {
	value.CreatedAt = value.CreatedAt.UTC().Truncate(time.Microsecond)
	if value.LastObservation != nil {
		copy := *value.LastObservation
		copy.ObservedAt = copy.ObservedAt.UTC().Truncate(time.Microsecond)
		value.LastObservation = &copy
	}
	value.LastAttempt.StartedAt = value.LastAttempt.StartedAt.UTC().Truncate(time.Microsecond)
	value.LastAttempt.CompletedAt = value.LastAttempt.CompletedAt.UTC().Truncate(time.Microsecond)
	if err := validateDeliveryReconciliationCaseShape(value, true); err != nil {
		return DeliveryReconciliationCase{}, err
	}
	hash, err := domain.CanonicalHash(deliveryReconciliationCaseHashPayload(value))
	if err != nil || value.CaseHash != "sha256:"+hash {
		return DeliveryReconciliationCase{}, invalid("delivery reconciliation Case hash")
	}
	return value, nil
}

func validateDeliveryReconciliationCaseShape(value DeliveryReconciliationCase, requireHash bool) error {
	if value.SchemaVersion != DeliveryReconciliationCaseSchemaVersion ||
		!validUUID(value.ID) || !validUUID(value.ProjectID) || !validUUID(value.RunID) ||
		!validUUID(value.OperationID) || !validUUID(value.ActorID) ||
		(value.RunKind != DeliveryOperationPreview && value.RunKind != DeliveryOperationProduction) ||
		value.ExpectedRunVersion == 0 || value.ExpectedRunVersion > uint64(^uint64(0)>>1) ||
		!exactHash(value.OperationRequestHash) || value.PreviousRemoteState != "quarantined" ||
		(value.ResumeRemoteState != "submit_unknown" && value.ResumeRemoteState != "accepted" && value.ResumeRemoteState != "running") ||
		!boundedIdentifier(value.Controller.ID, 200) || !boundedIdentifier(value.Controller.Version, 120) ||
		value.Controller.SchemaVersion != DeliveryControllerIdentitySchemaVersion ||
		value.Controller.Protocol != DeliveryControllerProtocolV3 || !exactHash(value.Controller.TrustKeyDigest) ||
		value.LastAttempt.Ordinal == 0 || !boundedIdentifier(string(value.LastAttempt.Kind), 20) ||
		!boundedIdentifier(value.LastAttempt.WorkerID, 200) || value.LastAttempt.FenceEpoch == 0 ||
		value.LastAttempt.StartedAt.IsZero() || value.LastAttempt.CompletedAt.IsZero() ||
		value.LastAttempt.Outcome != "quarantined" || !boundedIdentifier(value.LastAttempt.ErrorCode, 128) ||
		!boundedText(value.LastAttempt.ErrorDetail, 4000) ||
		!boundedIdentifier(value.QuarantineError.Code, 128) || !boundedText(value.QuarantineError.Detail, 4000) ||
		value.QuarantineError.Code != value.LastAttempt.ErrorCode || value.QuarantineError.Detail != value.LastAttempt.ErrorDetail ||
		!boundedIdentifier(value.IdempotencyKey, 128) || !exactHash(value.RequestHash) ||
		!boundedText(value.Reason, 1000) || value.CreatedAt.IsZero() {
		return invalid("delivery reconciliation Case")
	}
	if value.RunKind == DeliveryOperationPreview && value.RunSchemaVersion != "release-preview-run/v2" {
		return invalid("preview delivery reconciliation Run schema")
	}
	if value.RunKind == DeliveryOperationProduction && value.RunSchemaVersion != "release-deployment-run/v2" {
		return invalid("production delivery reconciliation Run schema")
	}
	if value.LastAttempt.Kind != DeliveryAttemptSubmit && value.LastAttempt.Kind != DeliveryAttemptReconcile &&
		value.LastAttempt.Kind != DeliveryAttemptResubmit {
		return invalid("delivery reconciliation Attempt kind")
	}
	if value.LastObservation != nil && (value.LastObservation.Sequence == 0 || value.LastObservation.ObservedAt.IsZero()) {
		return invalid("delivery reconciliation last observation")
	}
	if (value.ResumeRemoteState == "accepted" || value.ResumeRemoteState == "running") && value.LastObservation == nil {
		return invalid("delivery reconciliation observed resume state")
	}
	if requireHash && !exactHash(value.CaseHash) {
		return invalid("delivery reconciliation Case hash")
	}
	return nil
}

func deliveryReconciliationCaseHashPayload(value DeliveryReconciliationCase) any {
	copy := value
	copy.CaseHash = ""
	return copy
}

type ResumeBlockedDeliveryRequest struct {
	ProjectID         string                `json:"projectId"`
	RunKind           DeliveryOperationKind `json:"runKind"`
	RunID             string                `json:"runId"`
	ExpectedVersion   uint64                `json:"expectedVersion"`
	ExpectedErrorCode string                `json:"expectedErrorCode"`
	Reason            string                `json:"reason"`
	ActorID           string                `json:"-"`
	OperationID       string                `json:"-"`
}

type ResumeBlockedDeliveryInput struct {
	ID                string
	ProjectID         string
	RunKind           DeliveryOperationKind
	RunID             string
	ExpectedVersion   uint64
	ExpectedErrorCode string
	Reason            string
	ActorID           string
	IdempotencyKey    string
	RequestHash       string
}

type DeliveryReconciliationMutationStore interface {
	ResumeBlockedDelivery(context.Context, ResumeBlockedDeliveryInput) (DeliveryReconciliationCase, bool, error)
}

type DeliveryReconciliationReadStore interface {
	GetDeliveryReconciliationCase(context.Context, string, string) (DeliveryReconciliationCase, error)
	ListDeliveryReconciliationCases(context.Context, string, int) ([]DeliveryReconciliationCase, error)
	GetBlockedDeliveryReconciliationSnapshot(context.Context, string, DeliveryOperationKind, string) (DeliveryReconciliationBlockSnapshot, error)
}

func (service *DeliveryService) GetBlockedDeliveryReconciliationSnapshot(
	ctx context.Context,
	projectID string,
	runKind DeliveryOperationKind,
	runID, actorID string,
) (DeliveryReconciliationBlockSnapshot, error) {
	projectID, runID, actorID = strings.TrimSpace(projectID), strings.TrimSpace(runID), strings.TrimSpace(actorID)
	if !validUUID(projectID) || !validUUID(runID) || !validUUID(actorID) ||
		(runKind != DeliveryOperationPreview && runKind != DeliveryOperationProduction) {
		return DeliveryReconciliationBlockSnapshot{}, invalid("get blocked delivery reconciliation snapshot")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionAdmin); err != nil {
		return DeliveryReconciliationBlockSnapshot{}, fmt.Errorf("authorize blocked release reconciliation snapshot: %w", err)
	}
	store, ok := service.store.(DeliveryReconciliationReadStore)
	if !ok {
		return DeliveryReconciliationBlockSnapshot{}, ErrDeliveryReconciliationDisabled
	}
	return store.GetBlockedDeliveryReconciliationSnapshot(ctx, projectID, runKind, runID)
}

func (service *DeliveryService) ResumeBlockedDelivery(
	ctx context.Context,
	request ResumeBlockedDeliveryRequest,
) (DeliveryReconciliationCase, bool, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.ExpectedErrorCode = strings.TrimSpace(request.ExpectedErrorCode)
	request.Reason = strings.TrimSpace(request.Reason)
	request.ActorID = strings.TrimSpace(request.ActorID)
	request.OperationID = strings.TrimSpace(request.OperationID)
	if !validUUID(request.ProjectID) || !validUUID(request.RunID) || !validUUID(request.ActorID) ||
		(request.RunKind != DeliveryOperationPreview && request.RunKind != DeliveryOperationProduction) ||
		request.ExpectedVersion == 0 || request.ExpectedVersion > uint64(^uint64(0)>>1) ||
		!boundedIdentifier(request.ExpectedErrorCode, 128) || !boundedText(request.Reason, 1000) ||
		!boundedIdentifier(request.OperationID, 128) {
		return DeliveryReconciliationCase{}, false, invalid("blocked delivery reconciliation request")
	}
	if _, err := service.access.Authorize(ctx, request.ProjectID, request.ActorID, core.ActionAdmin); err != nil {
		return DeliveryReconciliationCase{}, false, fmt.Errorf("authorize blocked release reconciliation: %w", err)
	}
	store, ok := service.store.(DeliveryReconciliationMutationStore)
	if !ok {
		return DeliveryReconciliationCase{}, false, ErrDeliveryReconciliationDisabled
	}
	requestHash, err := deliveryRequestHash(map[string]any{
		"operation": "resume-blocked-delivery", "projectId": request.ProjectID,
		"runKind": request.RunKind, "runId": request.RunID,
		"expectedVersion": request.ExpectedVersion, "expectedErrorCode": request.ExpectedErrorCode,
		"reason": request.Reason,
	})
	if err != nil {
		return DeliveryReconciliationCase{}, false, err
	}
	caseID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(
		"release-delivery-reconciliation\x00"+request.ProjectID+"\x00"+request.OperationID,
	)).String()
	return store.ResumeBlockedDelivery(ctx, ResumeBlockedDeliveryInput{
		ID: caseID, ProjectID: request.ProjectID, RunKind: request.RunKind, RunID: request.RunID,
		ExpectedVersion: request.ExpectedVersion, ExpectedErrorCode: request.ExpectedErrorCode,
		Reason: request.Reason, ActorID: request.ActorID,
		IdempotencyKey: request.OperationID, RequestHash: requestHash,
	})
}

func (service *DeliveryService) GetDeliveryReconciliationCase(
	ctx context.Context,
	projectID, caseID, actorID string,
) (DeliveryReconciliationCase, error) {
	if !validUUID(strings.TrimSpace(projectID)) || !validUUID(strings.TrimSpace(caseID)) ||
		!validUUID(strings.TrimSpace(actorID)) {
		return DeliveryReconciliationCase{}, invalid("get delivery reconciliation Case request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return DeliveryReconciliationCase{}, err
	}
	store, ok := service.store.(DeliveryReconciliationReadStore)
	if !ok {
		return DeliveryReconciliationCase{}, ErrDeliveryReconciliationDisabled
	}
	return store.GetDeliveryReconciliationCase(ctx, projectID, caseID)
}

func (service *DeliveryService) ListDeliveryReconciliationCases(
	ctx context.Context,
	projectID, actorID string,
) ([]DeliveryReconciliationCase, error) {
	if !validUUID(strings.TrimSpace(projectID)) || !validUUID(strings.TrimSpace(actorID)) {
		return nil, invalid("list delivery reconciliation Cases request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	store, ok := service.store.(DeliveryReconciliationReadStore)
	if !ok {
		return nil, ErrDeliveryReconciliationDisabled
	}
	return store.ListDeliveryReconciliationCases(ctx, projectID, 100)
}
