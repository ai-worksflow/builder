package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	DeliveryControllerIdentitySchemaVersion = "release-delivery-controller-identity/v1"
	DeliveryControllerProtocolV3            = "worksflow.release-delivery/v3"
	DeliveryOperationRequestSchemaVersion   = "release-delivery-operation-request/v3"
	DeliveryOperationDocumentSchemaVersion  = "release-delivery-operation-document/v3"
	DeliveryOperationObservationSchema      = "release-delivery-operation-observation/v1"
	DeliveryOperationResultSchemaVersion    = "release-delivery-operation-result/v1"

	maxDeliveryOperationDocumentBytes = 16 << 20
)

type DeliveryOperationKind string

const (
	DeliveryOperationPreview    DeliveryOperationKind = "preview"
	DeliveryOperationProduction DeliveryOperationKind = "production"
)

type DeliveryRemoteState string

const (
	DeliveryRemoteAccepted  DeliveryRemoteState = "accepted"
	DeliveryRemoteRunning   DeliveryRemoteState = "running"
	DeliveryRemoteCompleted DeliveryRemoteState = "completed"
	DeliveryRemoteRejected  DeliveryRemoteState = "rejected"
)

type DeliveryControllerIdentity struct {
	SchemaVersion  string `json:"schemaVersion"`
	ID             string `json:"id"`
	Version        string `json:"version"`
	Protocol       string `json:"protocol"`
	TrustKeyDigest string `json:"trustKeyDigest"`
}

type DeliveryOperationRequest struct {
	SchemaVersion   string                `json:"schemaVersion"`
	OperationID     string                `json:"operationId"`
	Kind            DeliveryOperationKind `json:"kind"`
	ProjectID       string                `json:"projectId"`
	RequestHash     string                `json:"requestHash"`
	RequestDocument json.RawMessage       `json:"requestDocument"`
}

type deliveryOperationDocument struct {
	SchemaVersion string                `json:"schemaVersion"`
	OperationID   string                `json:"operationId"`
	Kind          DeliveryOperationKind `json:"kind"`
	ProjectID     string                `json:"projectId"`
	Payload       json.RawMessage       `json:"payload"`
}

type DeliveryOperationObservation struct {
	SchemaVersion string                     `json:"schemaVersion"`
	Controller    DeliveryControllerIdentity `json:"controller"`
	OperationID   string                     `json:"operationId"`
	RequestHash   string                     `json:"requestHash"`
	State         DeliveryRemoteState        `json:"state"`
	Sequence      uint64                     `json:"sequence"`
	ObservedAt    time.Time                  `json:"observedAt"`
	Result        *DeliveryOperationResult   `json:"result,omitempty"`
}

type DeliveryOperationResult struct {
	SchemaVersion   string                     `json:"schemaVersion"`
	Controller      DeliveryControllerIdentity `json:"controller"`
	OperationID     string                     `json:"operationId"`
	RequestHash     string                     `json:"requestHash"`
	Kind            DeliveryOperationKind      `json:"kind"`
	ProjectID       string                     `json:"projectId"`
	Status          DeliveryRemoteState        `json:"status"`
	Provider        string                     `json:"provider,omitempty"`
	ProviderRef     string                     `json:"providerRef,omitempty"`
	PublicURL       string                     `json:"publicUrl,omitempty"`
	Checks          []PreviewCheck             `json:"checks"`
	PreviousHead    *repository.ExactReference `json:"previousHead,omitempty"`
	NoMutation      bool                       `json:"noMutation"`
	RejectionCode   string                     `json:"rejectionCode,omitempty"`
	RejectionDetail string                     `json:"rejectionDetail,omitempty"`
	CompletedAt     time.Time                  `json:"completedAt"`
	ResultHash      string                     `json:"resultHash"`
}

func NewDeliveryOperationRequest(
	operationID string,
	kind DeliveryOperationKind,
	projectID string,
	payload any,
) (DeliveryOperationRequest, error) {
	if !validUUID(operationID) || !validUUID(projectID) || !validDeliveryOperationKind(kind) || payload == nil {
		return DeliveryOperationRequest{}, invalid("delivery operation identity")
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil || rejectReleaseDuplicateJSONNames(rawPayload) != nil {
		return DeliveryOperationRequest{}, invalid("delivery operation payload JSON")
	}
	canonicalPayload, err := domain.CanonicalJSON(rawPayload)
	if err != nil || len(canonicalPayload) == 0 || len(canonicalPayload) > maxDeliveryOperationDocumentBytes || canonicalPayload[0] != '{' {
		return DeliveryOperationRequest{}, invalid("delivery operation payload")
	}
	document := deliveryOperationDocument{
		SchemaVersion: DeliveryOperationDocumentSchemaVersion,
		OperationID:   operationID, Kind: kind, ProjectID: projectID,
		Payload: append(json.RawMessage(nil), canonicalPayload...),
	}
	canonicalDocument, err := domain.CanonicalJSON(document)
	if err != nil || len(canonicalDocument) > maxDeliveryOperationDocumentBytes {
		return DeliveryOperationRequest{}, invalid("delivery operation document")
	}
	hash, err := domain.CanonicalHash(document)
	if err != nil {
		return DeliveryOperationRequest{}, invalid("delivery operation request hash")
	}
	return DeliveryOperationRequest{
		SchemaVersion: DeliveryOperationRequestSchemaVersion,
		OperationID:   operationID, Kind: kind, ProjectID: projectID,
		RequestHash:     "sha256:" + hash,
		RequestDocument: append(json.RawMessage(nil), canonicalDocument...),
	}, nil
}

func ParseDeliveryOperationRequest(request DeliveryOperationRequest) (DeliveryOperationRequest, error) {
	if request.SchemaVersion != DeliveryOperationRequestSchemaVersion || !validUUID(request.OperationID) ||
		!validUUID(request.ProjectID) || !validDeliveryOperationKind(request.Kind) || !exactHash(request.RequestHash) ||
		len(request.RequestDocument) == 0 || len(request.RequestDocument) > maxDeliveryOperationDocumentBytes {
		return DeliveryOperationRequest{}, invalid("delivery operation request")
	}
	var document deliveryOperationDocument
	if err := decodeReleaseStrictJSON(request.RequestDocument, &document); err != nil {
		return DeliveryOperationRequest{}, invalid("delivery operation request document")
	}
	if document.SchemaVersion != DeliveryOperationDocumentSchemaVersion || document.OperationID != request.OperationID ||
		document.Kind != request.Kind || document.ProjectID != request.ProjectID || len(document.Payload) == 0 ||
		len(document.Payload) > maxDeliveryOperationDocumentBytes || document.Payload[0] != '{' {
		return DeliveryOperationRequest{}, invalid("delivery operation request lineage")
	}
	canonicalPayload, err := canonicalRawJSONObject(document.Payload)
	if err != nil || !bytes.Equal(canonicalPayload, document.Payload) {
		return DeliveryOperationRequest{}, invalid("noncanonical delivery operation payload")
	}
	canonicalDocument, err := domain.CanonicalJSON(document)
	if err != nil || !bytes.Equal(canonicalDocument, request.RequestDocument) {
		return DeliveryOperationRequest{}, invalid("noncanonical delivery operation document")
	}
	hash, err := domain.CanonicalHash(document)
	if err != nil || request.RequestHash != "sha256:"+hash {
		return DeliveryOperationRequest{}, invalid("delivery operation request hash")
	}
	request.RequestDocument = append(json.RawMessage(nil), request.RequestDocument...)
	return request, nil
}

func ParseDeliveryControllerIdentity(identity DeliveryControllerIdentity) (DeliveryControllerIdentity, error) {
	if identity.SchemaVersion != DeliveryControllerIdentitySchemaVersion || identity.Protocol != DeliveryControllerProtocolV3 ||
		!boundedIdentifier(identity.ID, 200) || !boundedIdentifier(identity.Version, 120) ||
		!exactHash(identity.TrustKeyDigest) {
		return DeliveryControllerIdentity{}, invalid("delivery controller identity")
	}
	return identity, nil
}

func ParseDeliveryOperationObservation(
	observation DeliveryOperationObservation,
	expected DeliveryControllerIdentity,
	request DeliveryOperationRequest,
) (DeliveryOperationObservation, error) {
	parsedExpected, err := ParseDeliveryControllerIdentity(expected)
	if err != nil {
		return DeliveryOperationObservation{}, err
	}
	request, err = ParseDeliveryOperationRequest(request)
	if err != nil {
		return DeliveryOperationObservation{}, err
	}
	controller, err := ParseDeliveryControllerIdentity(observation.Controller)
	if err != nil || controller != parsedExpected || observation.SchemaVersion != DeliveryOperationObservationSchema ||
		observation.OperationID != request.OperationID || observation.RequestHash != request.RequestHash ||
		observation.Sequence == 0 || observation.ObservedAt.IsZero() ||
		!observation.ObservedAt.Equal(observation.ObservedAt.UTC().Truncate(time.Microsecond)) {
		return DeliveryOperationObservation{}, invalid("delivery operation observation lineage")
	}
	switch observation.State {
	case DeliveryRemoteAccepted, DeliveryRemoteRunning:
		if observation.Result != nil {
			return DeliveryOperationObservation{}, invalid("nonterminal delivery operation result")
		}
	case DeliveryRemoteCompleted, DeliveryRemoteRejected:
		if observation.Result == nil {
			return DeliveryOperationObservation{}, invalid("missing terminal delivery operation result")
		}
		result, resultErr := ParseDeliveryOperationResult(*observation.Result, parsedExpected, request)
		if resultErr != nil || result.Status != observation.State || result.CompletedAt.After(observation.ObservedAt) {
			return DeliveryOperationObservation{}, invalid("terminal delivery operation result")
		}
		observation.Result = &result
	default:
		return DeliveryOperationObservation{}, invalid("delivery operation remote state")
	}
	observation.ObservedAt = observation.ObservedAt.UTC().Truncate(time.Microsecond)
	return observation, nil
}

func ParseDeliveryOperationResult(
	result DeliveryOperationResult,
	expected DeliveryControllerIdentity,
	request DeliveryOperationRequest,
) (DeliveryOperationResult, error) {
	var err error
	expected, err = ParseDeliveryControllerIdentity(expected)
	if err != nil {
		return DeliveryOperationResult{}, err
	}
	request, err = ParseDeliveryOperationRequest(request)
	if err != nil {
		return DeliveryOperationResult{}, err
	}
	if result.SchemaVersion != DeliveryOperationResultSchemaVersion || result.Controller != expected ||
		result.OperationID != request.OperationID || result.RequestHash != request.RequestHash ||
		result.Kind != request.Kind || result.ProjectID != request.ProjectID || result.CompletedAt.IsZero() ||
		!result.CompletedAt.Equal(result.CompletedAt.UTC().Truncate(time.Microsecond)) || !exactHash(result.ResultHash) {
		return DeliveryOperationResult{}, invalid("delivery operation result lineage")
	}
	switch result.Status {
	case DeliveryRemoteRejected:
		if !result.NoMutation || !boundedIdentifier(result.RejectionCode, 160) ||
			!boundedText(result.RejectionDetail, 2000) || result.Provider != "" || result.ProviderRef != "" ||
			result.RejectionCode != strings.TrimSpace(result.RejectionCode) ||
			result.RejectionDetail != strings.TrimSpace(result.RejectionDetail) ||
			result.PublicURL != "" || result.Checks == nil || len(result.Checks) != 0 ||
			result.PreviousHead != nil {
			return DeliveryOperationResult{}, invalid("delivery operation rejection")
		}
	case DeliveryRemoteCompleted:
		checks, checksErr := normalizePreviewChecks(result.Checks)
		if checksErr != nil || !samePreviewChecks(checks, result.Checks) {
			return DeliveryOperationResult{}, invalid("delivery operation result checks")
		}
		result.Checks = checks
		if result.NoMutation || result.RejectionCode != "" || result.RejectionDetail != "" ||
			!boundedIdentifier(result.Provider, 128) || !boundedIdentifier(result.ProviderRef, 1000) || len(result.Checks) == 0 ||
			result.Provider != strings.TrimSpace(result.Provider) ||
			result.ProviderRef != strings.TrimSpace(result.ProviderRef) ||
			result.PublicURL != strings.TrimSpace(result.PublicURL) ||
			len(result.PublicURL) > 2000 || strings.ContainsRune(result.PublicURL, '\x00') {
			return DeliveryOperationResult{}, invalid("completed delivery operation result")
		}
		if result.Kind == DeliveryOperationPreview {
			if result.PublicURL != "" || result.PreviousHead != nil {
				return DeliveryOperationResult{}, invalid("preview delivery operation result")
			}
		} else {
			expectedHead, headErr := expectedProductionHeadFromDeliveryRequest(request)
			if headErr != nil || validateOptionalExactReference(result.PreviousHead) != nil ||
				!sameOptionalExactReferences(result.PreviousHead, expectedHead.Revision) {
				return DeliveryOperationResult{}, invalid("production delivery operation head result")
			}
			if previewChecksPassed(result.Checks) && !boundedIdentifier(result.PublicURL, 2000) {
				return DeliveryOperationResult{}, invalid("healthy production delivery operation URL")
			}
		}
	default:
		return DeliveryOperationResult{}, invalid("delivery operation terminal status")
	}
	hash, err := domain.CanonicalHash(deliveryOperationResultHashPayload(result))
	if err != nil || result.ResultHash != "sha256:"+hash {
		return DeliveryOperationResult{}, invalid("delivery operation result hash")
	}
	result.CompletedAt = result.CompletedAt.UTC().Truncate(time.Microsecond)
	return result, nil
}

func expectedProductionHeadFromDeliveryRequest(
	request DeliveryOperationRequest,
) (ExpectedProductionHead, error) {
	var document deliveryOperationDocument
	if err := decodeReleaseStrictJSON(request.RequestDocument, &document); err != nil {
		return ExpectedProductionHead{}, err
	}
	var envelope struct {
		ExpectedHead json.RawMessage `json:"expectedHead"`
	}
	if err := json.Unmarshal(document.Payload, &envelope); err != nil || len(envelope.ExpectedHead) == 0 {
		return ExpectedProductionHead{}, errors.New("production delivery request has no expected head")
	}
	var expected ExpectedProductionHead
	if err := decodeReleaseStrictJSON(envelope.ExpectedHead, &expected); err != nil ||
		validateOptionalExactReference(expected.Revision) != nil ||
		validateOptionalExactReference(expected.ProductionReceipt) != nil ||
		(expected.Revision == nil) != (expected.ProductionReceipt == nil) {
		return ExpectedProductionHead{}, errors.New("production delivery request expected head is invalid")
	}
	return cloneExpectedProductionHead(expected), nil
}

func deliveryOperationResultHashPayload(result DeliveryOperationResult) any {
	copy := result
	copy.ResultHash = ""
	return copy
}

func validDeliveryOperationKind(kind DeliveryOperationKind) bool {
	return kind == DeliveryOperationPreview || kind == DeliveryOperationProduction
}

func validateOptionalExactReference(reference *repository.ExactReference) error {
	if reference == nil {
		return nil
	}
	if !validUUID(reference.ID) || !exactHash(reference.ContentHash) {
		return errors.New("invalid exact reference")
	}
	return nil
}

func canonicalRawJSONObject(input json.RawMessage) ([]byte, error) {
	var value map[string]json.RawMessage
	if err := decodeReleaseStrictJSON(input, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("JSON object is required")
	}
	encoded, err := domain.CanonicalJSON(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize JSON object: %w", err)
	}
	return encoded, nil
}
