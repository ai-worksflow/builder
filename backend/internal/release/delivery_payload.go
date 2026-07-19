package release

import (
	"errors"
	"strings"

	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	PreviewDeliveryOperationPayloadSchema    = "release-preview-operation-payload/v1"
	ProductionDeliveryOperationPayloadSchema = "release-production-operation-payload/v1"
)

// PreviewDeliveryOperationPayload is the complete immutable controller input.
// It embeds the exact ReleaseBundle so a crash or serializer upgrade can never
// cause a later attempt to reconstruct different remote mutation bytes.
type PreviewDeliveryOperationPayload struct {
	SchemaVersion string `json:"schemaVersion"`
	OperationID   string `json:"operationId"`
	RunID         string `json:"runId"`
	ProjectID     string `json:"projectId"`
	Reason        string `json:"reason"`
	Namespace     string `json:"namespace"`
	ReleaseBundle Bundle `json:"releaseBundle"`
}

type ExpectedProductionHead struct {
	Revision          *repository.ExactReference `json:"revision"`
	ProductionReceipt *repository.ExactReference `json:"productionReceipt"`
}

// ProductionDeliveryOperationPayload carries every reviewed input and the
// exact local production-head precondition. SourceRevision is explicitly null
// for promotion; ExpectedHead members are explicitly null for an empty head.
type ProductionDeliveryOperationPayload struct {
	SchemaVersion     string                 `json:"schemaVersion"`
	OperationID       string                 `json:"operationId"`
	RunID             string                 `json:"runId"`
	ProjectID         string                 `json:"projectId"`
	Reason            string                 `json:"reason"`
	Environment       string                 `json:"environment"`
	Operation         DeploymentOperation    `json:"operation"`
	ReleaseBundle     Bundle                 `json:"releaseBundle"`
	PreviewReceipt    PreviewReceipt         `json:"previewReceipt"`
	PromotionApproval PromotionApproval      `json:"promotionApproval"`
	SourceRevision    *DeploymentRevision    `json:"sourceRevision"`
	ExpectedHead      ExpectedProductionHead `json:"expectedHead"`
}

func newPreviewDeliveryOperationRequest(
	runID string,
	bundle Bundle,
	reason, namespace string,
) (DeliveryOperationRequest, error) {
	parsedBundle, err := ParseBundle(bundle)
	if err != nil || !validUUID(runID) || !boundedText(reason, 1000) || !boundedIdentifier(namespace, 200) {
		return DeliveryOperationRequest{}, invalid("preview delivery operation payload")
	}
	payload := PreviewDeliveryOperationPayload{
		SchemaVersion: PreviewDeliveryOperationPayloadSchema,
		OperationID:   runID,
		RunID:         runID,
		ProjectID:     parsedBundle.ProjectID,
		Reason:        strings.TrimSpace(reason),
		Namespace:     strings.TrimSpace(namespace),
		ReleaseBundle: parsedBundle,
	}
	return NewDeliveryOperationRequest(runID, DeliveryOperationPreview, parsedBundle.ProjectID, payload)
}

func newProductionDeliveryOperationRequest(
	runID, environment, reason string,
	operation DeploymentOperation,
	bundle Bundle,
	preview PreviewReceipt,
	approval PromotionApproval,
	source *DeploymentRevision,
	expected ExpectedProductionHead,
) (DeliveryOperationRequest, error) {
	parsedBundle, err := ParseBundle(bundle)
	if err != nil {
		return DeliveryOperationRequest{}, invalid("production delivery Bundle")
	}
	parsedPreview, err := ParsePreviewReceipt(preview)
	if err != nil || parsedPreview.SchemaVersion != PreviewReceiptSchemaVersionV2 {
		return DeliveryOperationRequest{}, invalid("production delivery PreviewReceipt")
	}
	parsedApproval, err := ParsePromotionApproval(approval)
	if err != nil {
		return DeliveryOperationRequest{}, invalid("production delivery PromotionApproval")
	}
	var parsedSource *DeploymentRevision
	if source != nil {
		value, parseErr := ParseDeploymentRevision(*source)
		if parseErr != nil {
			return DeliveryOperationRequest{}, invalid("production delivery source DeploymentRevision")
		}
		parsedSource = &value
	}
	if !validUUID(runID) || !boundedIdentifier(environment, 120) || !boundedText(reason, 1000) ||
		(operation != DeploymentPromote && operation != DeploymentRollback) ||
		validateOptionalExactReference(expected.Revision) != nil ||
		validateOptionalExactReference(expected.ProductionReceipt) != nil ||
		(expected.Revision == nil) != (expected.ProductionReceipt == nil) {
		return DeliveryOperationRequest{}, invalid("production delivery operation identity or expected head")
	}
	if parsedBundle.ProjectID != parsedPreview.ProjectID || parsedBundle.ProjectID != parsedApproval.ProjectID ||
		parsedPreview.ReleaseBundle.ID != parsedBundle.ID || parsedPreview.ReleaseBundle.ContentHash != parsedBundle.BundleHash ||
		parsedApproval.ReleaseBundle != parsedPreview.ReleaseBundle ||
		parsedApproval.PreviewReceipt.ID != parsedPreview.ID ||
		parsedApproval.PreviewReceipt.ContentHash != parsedPreview.PayloadHash {
		return DeliveryOperationRequest{}, invalid("production delivery reviewed lineage")
	}
	if operation == DeploymentPromote && parsedSource != nil {
		return DeliveryOperationRequest{}, invalid("promotion source revision")
	}
	if operation == DeploymentRollback {
		if parsedSource == nil || parsedSource.ProjectID != parsedBundle.ProjectID ||
			parsedSource.SchemaVersion != DeploymentRevisionSchemaVersionV2 ||
			parsedSource.ReleaseBundle.ID != parsedBundle.ID ||
			parsedSource.ReleaseBundle.ContentHash != parsedBundle.BundleHash ||
			parsedSource.PreviewReceipt.ID != parsedPreview.ID ||
			parsedSource.PreviewReceipt.ContentHash != parsedPreview.PayloadHash ||
			parsedSource.Approval.ID != parsedApproval.ID ||
			parsedSource.Approval.ContentHash != parsedApproval.PayloadHash {
			return DeliveryOperationRequest{}, invalid("rollback source lineage")
		}
	}
	payload := ProductionDeliveryOperationPayload{
		SchemaVersion:     ProductionDeliveryOperationPayloadSchema,
		OperationID:       runID,
		RunID:             runID,
		ProjectID:         parsedBundle.ProjectID,
		Reason:            strings.TrimSpace(reason),
		Environment:       strings.TrimSpace(environment),
		Operation:         operation,
		ReleaseBundle:     parsedBundle,
		PreviewReceipt:    parsedPreview,
		PromotionApproval: parsedApproval,
		SourceRevision:    parsedSource,
		ExpectedHead:      cloneExpectedProductionHead(expected),
	}
	return NewDeliveryOperationRequest(runID, DeliveryOperationProduction, parsedBundle.ProjectID, payload)
}

func cloneExpectedProductionHead(value ExpectedProductionHead) ExpectedProductionHead {
	return ExpectedProductionHead{
		Revision:          cloneExactReference(value.Revision),
		ProductionReceipt: cloneExactReference(value.ProductionReceipt),
	}
}

func validateDeliveryControllerForStore(identity DeliveryControllerIdentity) (DeliveryControllerIdentity, error) {
	parsed, err := ParseDeliveryControllerIdentity(identity)
	if err != nil {
		return DeliveryControllerIdentity{}, errors.New("release delivery store requires an exact configured controller identity")
	}
	return parsed, nil
}
