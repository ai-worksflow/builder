package release

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/verification"
)

const (
	PreviewReceiptSchemaVersion       = "release-preview-receipt/v1"
	PreviewReceiptSchemaVersionV2     = "release-preview-receipt/v2"
	ProductionReceiptSchemaVersion    = "release-production-receipt/v1"
	ProductionReceiptSchemaVersionV2  = "release-production-receipt/v2"
	PromotionApprovalSchemaVersion    = "release-promotion-approval/v1"
	DeploymentRevisionSchemaVersion   = "release-deployment-revision/v1"
	DeploymentRevisionSchemaVersionV2 = "release-deployment-revision/v2"
)

type PreviewDecision string

const (
	PreviewPassed PreviewDecision = "passed"
	PreviewFailed PreviewDecision = "failed"
)

type PreviewCheck struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ControllerOperationResultReference binds a receipt or production head to
// one immutable v3 controller result, rather than merely copying provider
// strings and checks that could coincide across different operations.
type ControllerOperationResultReference struct {
	OperationID string `json:"operationId"`
	ResultHash  string `json:"resultHash"`
}

type NewPreviewReceiptInput struct {
	ID          string
	RunID       string
	Bundle      Bundle
	Namespace   string
	Provider    string
	ProviderRef string
	Checks      []PreviewCheck
	Decision    PreviewDecision
	CreatedBy   string
	CreatedAt   time.Time
}

// PreviewReceipt is immutable evidence that one exact ReleaseBundle was
// deployed and exercised in an isolated preview trust domain.
type PreviewReceipt struct {
	SchemaVersion       string                                  `json:"schemaVersion"`
	ID                  string                                  `json:"id"`
	RunID               string                                  `json:"runId"`
	ProjectID           string                                  `json:"projectId"`
	ReleaseBundle       repository.ExactReference               `json:"releaseBundle"`
	CanonicalReceipt    repository.ExactReference               `json:"canonicalReceipt"`
	Workspace           verification.CanonicalPlanSubject       `json:"workspace"`
	ReleaseArtifacts    []verification.CanonicalReleaseArtifact `json:"releaseArtifacts"`
	Namespace           string                                  `json:"namespace"`
	Provider            string                                  `json:"provider"`
	ProviderRef         string                                  `json:"providerRef"`
	Checks              []PreviewCheck                          `json:"checks"`
	Decision            PreviewDecision                         `json:"decision"`
	ControllerOperation *ControllerOperationResultReference     `json:"controllerOperation,omitempty"`
	PayloadHash         string                                  `json:"payloadHash"`
	CreatedBy           string                                  `json:"createdBy"`
	CreatedAt           time.Time                               `json:"createdAt"`
}

func NewPreviewReceipt(input NewPreviewReceiptInput) (PreviewReceipt, error) {
	return newPreviewReceipt(input, PreviewReceiptSchemaVersion, nil)
}

func NewPreviewReceiptV2(
	input NewPreviewReceiptInput,
	controllerOperation ControllerOperationResultReference,
) (PreviewReceipt, error) {
	return newPreviewReceipt(input, PreviewReceiptSchemaVersionV2, &controllerOperation)
}

func newPreviewReceipt(
	input NewPreviewReceiptInput,
	schemaVersion string,
	controllerOperation *ControllerOperationResultReference,
) (PreviewReceipt, error) {
	if err := validateControllerOperationResultReference(controllerOperation, schemaVersion == PreviewReceiptSchemaVersionV2); err != nil {
		return PreviewReceipt{}, err
	}
	bundle, err := ParseBundle(input.Bundle)
	if err != nil {
		return PreviewReceipt{}, fmt.Errorf("%w: preview Bundle: %v", ErrInvalidBundle, err)
	}
	if !validUUID(input.ID) || !validUUID(input.RunID) || !validUUID(input.CreatedBy) || input.CreatedAt.IsZero() {
		return PreviewReceipt{}, invalid("preview receipt identity")
	}
	if !boundedIdentifier(input.Namespace, 200) || !boundedIdentifier(input.Provider, 128) ||
		!boundedIdentifier(input.ProviderRef, 1000) {
		return PreviewReceipt{}, invalid("preview provider identity")
	}
	checks, err := normalizePreviewChecks(input.Checks)
	if err != nil {
		return PreviewReceipt{}, err
	}
	if input.Decision != PreviewPassed && input.Decision != PreviewFailed {
		return PreviewReceipt{}, invalid("preview decision")
	}
	allPassed := true
	for _, check := range checks {
		if check.Status != "passed" {
			allPassed = false
		}
	}
	if (input.Decision == PreviewPassed) != allPassed {
		return PreviewReceipt{}, invalid("preview check decision")
	}
	if input.Decision == PreviewPassed && !checksCoverKinds(checks, "migration", "health", "smoke", "contract", "e2e") {
		return PreviewReceipt{}, invalid("preview required checks")
	}
	receipt := PreviewReceipt{
		SchemaVersion: schemaVersion, ID: input.ID, RunID: input.RunID,
		ProjectID:        bundle.ProjectID,
		ReleaseBundle:    repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		CanonicalReceipt: bundle.CanonicalReceipt, Workspace: bundle.Workspace,
		ReleaseArtifacts: append([]verification.CanonicalReleaseArtifact(nil), bundle.ReleaseArtifacts...),
		Namespace:        strings.TrimSpace(input.Namespace), Provider: strings.TrimSpace(input.Provider),
		ProviderRef: strings.TrimSpace(input.ProviderRef), Checks: checks, Decision: input.Decision,
		ControllerOperation: cloneControllerOperationResultReference(controllerOperation),
		CreatedBy:           input.CreatedBy, CreatedAt: input.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	hash, err := domain.CanonicalHash(previewReceiptHashPayload(receipt))
	if err != nil {
		return PreviewReceipt{}, invalid("preview receipt payload")
	}
	receipt.PayloadHash = "sha256:" + hash
	return receipt, nil
}

func ParsePreviewReceipt(value PreviewReceipt) (PreviewReceipt, error) {
	if (value.SchemaVersion != PreviewReceiptSchemaVersion && value.SchemaVersion != PreviewReceiptSchemaVersionV2) ||
		validateControllerOperationResultReference(value.ControllerOperation, value.SchemaVersion == PreviewReceiptSchemaVersionV2) != nil ||
		!exactHash(value.PayloadHash) ||
		!validUUID(value.ID) || !validUUID(value.RunID) || !validUUID(value.ProjectID) ||
		!validUUID(value.CreatedBy) || value.CreatedAt.IsZero() {
		return PreviewReceipt{}, invalid("preview receipt envelope")
	}
	if !validUUID(value.ReleaseBundle.ID) || !exactHash(value.ReleaseBundle.ContentHash) ||
		!validUUID(value.CanonicalReceipt.ID) || !exactHash(value.CanonicalReceipt.ContentHash) ||
		!validUUID(value.Workspace.WorkspaceArtifactID) || !validUUID(value.Workspace.WorkspaceRevisionID) ||
		!exactHash(value.Workspace.WorkspaceContentHash) ||
		!boundedIdentifier(value.Namespace, 200) || !boundedIdentifier(value.Provider, 128) ||
		!boundedIdentifier(value.ProviderRef, 1000) || !validDeploymentArtifacts(value.ReleaseArtifacts) {
		return PreviewReceipt{}, invalid("preview receipt lineage")
	}
	checks, err := normalizePreviewChecks(value.Checks)
	if err != nil || !samePreviewChecks(checks, value.Checks) {
		return PreviewReceipt{}, invalid("preview receipt checks")
	}
	allPassed := true
	for _, check := range checks {
		if check.Status != "passed" {
			allPassed = false
		}
	}
	if (value.Decision == PreviewPassed) != allPassed ||
		(value.Decision != PreviewPassed && value.Decision != PreviewFailed) {
		return PreviewReceipt{}, invalid("preview receipt decision")
	}
	if value.Decision == PreviewPassed && !checksCoverKinds(checks, "migration", "health", "smoke", "contract", "e2e") {
		return PreviewReceipt{}, invalid("preview receipt required checks")
	}
	hash, err := domain.CanonicalHash(previewReceiptHashPayload(value))
	if err != nil || value.PayloadHash != "sha256:"+hash {
		return PreviewReceipt{}, invalid("preview receipt hash")
	}
	value.CreatedAt = value.CreatedAt.UTC().Truncate(time.Microsecond)
	return value, nil
}

func (receipt PreviewReceipt) PassedReference() (repository.ExactReference, error) {
	parsed, err := ParsePreviewReceipt(receipt)
	if err != nil || parsed.Decision != PreviewPassed {
		return repository.ExactReference{}, invalid("passing preview receipt")
	}
	return repository.ExactReference{ID: parsed.ID, ContentHash: parsed.PayloadHash}, nil
}

type NewPromotionApprovalInput struct {
	ID        string
	Preview   PreviewReceipt
	Reason    string
	CreatedBy string
	CreatedAt time.Time
}

type PromotionApproval struct {
	SchemaVersion  string                    `json:"schemaVersion"`
	ID             string                    `json:"id"`
	ProjectID      string                    `json:"projectId"`
	ReleaseBundle  repository.ExactReference `json:"releaseBundle"`
	PreviewReceipt repository.ExactReference `json:"previewReceipt"`
	Reason         string                    `json:"reason"`
	PayloadHash    string                    `json:"payloadHash"`
	CreatedBy      string                    `json:"createdBy"`
	CreatedAt      time.Time                 `json:"createdAt"`
}

func NewPromotionApproval(input NewPromotionApprovalInput) (PromotionApproval, error) {
	preview, err := ParsePreviewReceipt(input.Preview)
	if err != nil || preview.Decision != PreviewPassed || !validUUID(input.ID) ||
		!validUUID(input.CreatedBy) || input.CreatedAt.IsZero() || !boundedText(input.Reason, 1000) {
		return PromotionApproval{}, invalid("promotion approval input")
	}
	previewRef, _ := preview.PassedReference()
	approval := PromotionApproval{
		SchemaVersion: PromotionApprovalSchemaVersion, ID: input.ID, ProjectID: preview.ProjectID,
		ReleaseBundle: preview.ReleaseBundle, PreviewReceipt: previewRef,
		Reason: strings.TrimSpace(input.Reason), CreatedBy: input.CreatedBy,
		CreatedAt: input.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	hash, err := domain.CanonicalHash(promotionApprovalHashPayload(approval))
	if err != nil {
		return PromotionApproval{}, invalid("promotion approval payload")
	}
	approval.PayloadHash = "sha256:" + hash
	return approval, nil
}

func ParsePromotionApproval(value PromotionApproval) (PromotionApproval, error) {
	if value.SchemaVersion != PromotionApprovalSchemaVersion || !validUUID(value.ID) ||
		!validUUID(value.ProjectID) || !validUUID(value.CreatedBy) || value.CreatedAt.IsZero() ||
		!validUUID(value.ReleaseBundle.ID) || !exactHash(value.ReleaseBundle.ContentHash) ||
		!validUUID(value.PreviewReceipt.ID) || !exactHash(value.PreviewReceipt.ContentHash) ||
		!boundedText(value.Reason, 1000) || !exactHash(value.PayloadHash) {
		return PromotionApproval{}, invalid("promotion approval envelope")
	}
	hash, err := domain.CanonicalHash(promotionApprovalHashPayload(value))
	if err != nil || value.PayloadHash != "sha256:"+hash {
		return PromotionApproval{}, invalid("promotion approval hash")
	}
	value.CreatedAt = value.CreatedAt.UTC().Truncate(time.Microsecond)
	return value, nil
}

type DeploymentOperation string

const (
	DeploymentPromote  DeploymentOperation = "promote"
	DeploymentRollback DeploymentOperation = "rollback"
)

type NewProductionReceiptInput struct {
	ID             string
	RunID          string
	Bundle         Bundle
	Preview        PreviewReceipt
	Approval       PromotionApproval
	Operation      DeploymentOperation
	SourceRevision *repository.ExactReference
	Provider       string
	ProviderRef    string
	PublicURL      string
	Checks         []PreviewCheck
	Decision       PreviewDecision
	CreatedBy      string
	CreatedAt      time.Time
}

type ProductionReceipt struct {
	SchemaVersion       string                              `json:"schemaVersion"`
	ID                  string                              `json:"id"`
	RunID               string                              `json:"runId"`
	ProjectID           string                              `json:"projectId"`
	Operation           DeploymentOperation                 `json:"operation"`
	ReleaseBundle       repository.ExactReference           `json:"releaseBundle"`
	PreviewReceipt      repository.ExactReference           `json:"previewReceipt"`
	Approval            repository.ExactReference           `json:"promotionApproval"`
	SourceRevision      *repository.ExactReference          `json:"sourceRevision,omitempty"`
	Provider            string                              `json:"provider"`
	ProviderRef         string                              `json:"providerRef"`
	PublicURL           string                              `json:"publicUrl"`
	Checks              []PreviewCheck                      `json:"checks"`
	Decision            PreviewDecision                     `json:"decision"`
	ControllerOperation *ControllerOperationResultReference `json:"controllerOperation,omitempty"`
	PayloadHash         string                              `json:"payloadHash"`
	CreatedBy           string                              `json:"createdBy"`
	CreatedAt           time.Time                           `json:"createdAt"`
}

func NewProductionReceipt(input NewProductionReceiptInput) (ProductionReceipt, error) {
	return newProductionReceipt(input, ProductionReceiptSchemaVersion, nil)
}

func NewProductionReceiptV2(
	input NewProductionReceiptInput,
	controllerOperation ControllerOperationResultReference,
) (ProductionReceipt, error) {
	return newProductionReceipt(input, ProductionReceiptSchemaVersionV2, &controllerOperation)
}

func newProductionReceipt(
	input NewProductionReceiptInput,
	schemaVersion string,
	controllerOperation *ControllerOperationResultReference,
) (ProductionReceipt, error) {
	if err := validateControllerOperationResultReference(controllerOperation, schemaVersion == ProductionReceiptSchemaVersionV2); err != nil {
		return ProductionReceipt{}, err
	}
	bundle, err := ParseBundle(input.Bundle)
	if err != nil {
		return ProductionReceipt{}, invalid("production receipt Bundle")
	}
	preview, err := ParsePreviewReceipt(input.Preview)
	if err != nil || preview.Decision != PreviewPassed || preview.ReleaseBundle.ID != bundle.ID ||
		preview.ReleaseBundle.ContentHash != bundle.BundleHash || !sameArtifacts(preview.ReleaseArtifacts, bundle.ReleaseArtifacts) {
		return ProductionReceipt{}, invalid("production receipt preview")
	}
	if (schemaVersion == ProductionReceiptSchemaVersionV2) != (preview.SchemaVersion == PreviewReceiptSchemaVersionV2) {
		return ProductionReceipt{}, invalid("production receipt preview authority generation")
	}
	approval, err := ParsePromotionApproval(input.Approval)
	if err != nil || approval.ProjectID != bundle.ProjectID || approval.ReleaseBundle != preview.ReleaseBundle ||
		approval.PreviewReceipt.ID != preview.ID || approval.PreviewReceipt.ContentHash != preview.PayloadHash {
		return ProductionReceipt{}, invalid("production receipt approval")
	}
	if !validUUID(input.ID) || !validUUID(input.RunID) || !validUUID(input.CreatedBy) || input.CreatedAt.IsZero() ||
		!boundedIdentifier(input.Provider, 128) || !boundedIdentifier(input.ProviderRef, 1000) ||
		len(strings.TrimSpace(input.PublicURL)) > 2000 || strings.ContainsRune(input.PublicURL, '\x00') {
		return ProductionReceipt{}, invalid("production receipt identity")
	}
	if err := validateDeploymentOperationSource(input.Operation, input.SourceRevision); err != nil {
		return ProductionReceipt{}, err
	}
	checks, err := normalizePreviewChecks(input.Checks)
	if err != nil {
		return ProductionReceipt{}, invalid("production receipt checks")
	}
	allPassed := previewChecksPassed(checks)
	if (input.Decision == PreviewPassed) != allPassed ||
		(input.Decision != PreviewPassed && input.Decision != PreviewFailed) ||
		(input.Decision == PreviewPassed && !boundedIdentifier(input.PublicURL, 2000)) {
		return ProductionReceipt{}, invalid("production receipt decision")
	}
	if input.Decision == PreviewPassed && !checksCoverKinds(checks, "health", "rollout") {
		return ProductionReceipt{}, invalid("production receipt required checks")
	}
	previewRef, _ := preview.PassedReference()
	receipt := ProductionReceipt{
		SchemaVersion: schemaVersion, ID: input.ID, RunID: input.RunID,
		ProjectID: bundle.ProjectID, Operation: input.Operation,
		ReleaseBundle:  repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		PreviewReceipt: previewRef,
		Approval:       repository.ExactReference{ID: approval.ID, ContentHash: approval.PayloadHash},
		SourceRevision: cloneExactReference(input.SourceRevision), Provider: strings.TrimSpace(input.Provider),
		ProviderRef: strings.TrimSpace(input.ProviderRef), PublicURL: strings.TrimSpace(input.PublicURL),
		Checks: checks, Decision: input.Decision,
		ControllerOperation: cloneControllerOperationResultReference(controllerOperation), CreatedBy: input.CreatedBy,
		CreatedAt: input.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	hash, err := domain.CanonicalHash(productionReceiptHashPayload(receipt))
	if err != nil {
		return ProductionReceipt{}, invalid("production receipt payload")
	}
	receipt.PayloadHash = "sha256:" + hash
	return receipt, nil
}

func ParseProductionReceipt(value ProductionReceipt) (ProductionReceipt, error) {
	if (value.SchemaVersion != ProductionReceiptSchemaVersion && value.SchemaVersion != ProductionReceiptSchemaVersionV2) ||
		validateControllerOperationResultReference(value.ControllerOperation, value.SchemaVersion == ProductionReceiptSchemaVersionV2) != nil ||
		!validUUID(value.ID) || !validUUID(value.RunID) ||
		!validUUID(value.ProjectID) || !validUUID(value.CreatedBy) || value.CreatedAt.IsZero() ||
		!validUUID(value.ReleaseBundle.ID) || !exactHash(value.ReleaseBundle.ContentHash) ||
		!validUUID(value.PreviewReceipt.ID) || !exactHash(value.PreviewReceipt.ContentHash) ||
		!validUUID(value.Approval.ID) || !exactHash(value.Approval.ContentHash) ||
		!boundedIdentifier(value.Provider, 128) || !boundedIdentifier(value.ProviderRef, 1000) ||
		len(value.PublicURL) > 2000 || strings.ContainsRune(value.PublicURL, '\x00') || !exactHash(value.PayloadHash) {
		return ProductionReceipt{}, invalid("production receipt envelope")
	}
	if err := validateDeploymentOperationSource(value.Operation, value.SourceRevision); err != nil {
		return ProductionReceipt{}, err
	}
	checks, err := normalizePreviewChecks(value.Checks)
	if err != nil || !samePreviewChecks(checks, value.Checks) {
		return ProductionReceipt{}, invalid("production receipt checks")
	}
	if (value.Decision == PreviewPassed) != previewChecksPassed(checks) ||
		(value.Decision != PreviewPassed && value.Decision != PreviewFailed) ||
		(value.Decision == PreviewPassed && !boundedIdentifier(value.PublicURL, 2000)) {
		return ProductionReceipt{}, invalid("production receipt decision")
	}
	if value.Decision == PreviewPassed && !checksCoverKinds(checks, "health", "rollout") {
		return ProductionReceipt{}, invalid("production receipt required checks")
	}
	hash, err := domain.CanonicalHash(productionReceiptHashPayload(value))
	if err != nil || value.PayloadHash != "sha256:"+hash {
		return ProductionReceipt{}, invalid("production receipt hash")
	}
	value.CreatedAt = value.CreatedAt.UTC().Truncate(time.Microsecond)
	return value, nil
}

func (receipt ProductionReceipt) PassedReference() (repository.ExactReference, error) {
	parsed, err := ParseProductionReceipt(receipt)
	if err != nil || parsed.Decision != PreviewPassed {
		return repository.ExactReference{}, invalid("passing production receipt")
	}
	return repository.ExactReference{ID: parsed.ID, ContentHash: parsed.PayloadHash}, nil
}

type NewDeploymentRevisionInput struct {
	ID             string
	RunID          string
	Bundle         Bundle
	Preview        PreviewReceipt
	Approval       PromotionApproval
	Receipt        ProductionReceipt
	Operation      DeploymentOperation
	SourceRevision *repository.ExactReference
	Provider       string
	ProviderRef    string
	PublicURL      string
	Checks         []PreviewCheck
	CreatedBy      string
	CreatedAt      time.Time
}

type DeploymentRevision struct {
	SchemaVersion       string                              `json:"schemaVersion"`
	ID                  string                              `json:"id"`
	RunID               string                              `json:"runId"`
	ProjectID           string                              `json:"projectId"`
	ReleaseBundle       repository.ExactReference           `json:"releaseBundle"`
	PreviewReceipt      repository.ExactReference           `json:"previewReceipt"`
	Approval            repository.ExactReference           `json:"promotionApproval"`
	ProductionReceipt   repository.ExactReference           `json:"productionReceipt"`
	Operation           DeploymentOperation                 `json:"operation"`
	SourceRevision      *repository.ExactReference          `json:"sourceRevision,omitempty"`
	Provider            string                              `json:"provider"`
	ProviderRef         string                              `json:"providerRef"`
	PublicURL           string                              `json:"publicUrl"`
	Checks              []PreviewCheck                      `json:"checks"`
	ControllerOperation *ControllerOperationResultReference `json:"controllerOperation,omitempty"`
	PayloadHash         string                              `json:"payloadHash"`
	CreatedBy           string                              `json:"createdBy"`
	CreatedAt           time.Time                           `json:"createdAt"`
}

func NewDeploymentRevision(input NewDeploymentRevisionInput) (DeploymentRevision, error) {
	return newDeploymentRevision(input, DeploymentRevisionSchemaVersion, nil)
}

func NewDeploymentRevisionV2(
	input NewDeploymentRevisionInput,
	controllerOperation ControllerOperationResultReference,
) (DeploymentRevision, error) {
	return newDeploymentRevision(input, DeploymentRevisionSchemaVersionV2, &controllerOperation)
}

func newDeploymentRevision(
	input NewDeploymentRevisionInput,
	schemaVersion string,
	controllerOperation *ControllerOperationResultReference,
) (DeploymentRevision, error) {
	if err := validateControllerOperationResultReference(controllerOperation, schemaVersion == DeploymentRevisionSchemaVersionV2); err != nil {
		return DeploymentRevision{}, err
	}
	bundle, err := ParseBundle(input.Bundle)
	if err != nil {
		return DeploymentRevision{}, invalid("deployment Bundle")
	}
	preview, err := ParsePreviewReceipt(input.Preview)
	if err != nil || preview.Decision != PreviewPassed {
		return DeploymentRevision{}, invalid("deployment preview")
	}
	if (schemaVersion == DeploymentRevisionSchemaVersionV2) != (preview.SchemaVersion == PreviewReceiptSchemaVersionV2) {
		return DeploymentRevision{}, invalid("deployment preview authority generation")
	}
	approval, err := ParsePromotionApproval(input.Approval)
	if err != nil || approval.ProjectID != bundle.ProjectID || approval.ReleaseBundle.ID != bundle.ID ||
		approval.ReleaseBundle.ContentHash != bundle.BundleHash || approval.PreviewReceipt.ID != preview.ID ||
		approval.PreviewReceipt.ContentHash != preview.PayloadHash {
		return DeploymentRevision{}, invalid("deployment promotion approval")
	}
	receipt, err := ParseProductionReceipt(input.Receipt)
	if err != nil || receipt.Decision != PreviewPassed || receipt.RunID != input.RunID ||
		receipt.ProjectID != bundle.ProjectID || receipt.ReleaseBundle.ID != bundle.ID ||
		receipt.ReleaseBundle.ContentHash != bundle.BundleHash || receipt.PreviewReceipt.ID != preview.ID ||
		receipt.PreviewReceipt.ContentHash != preview.PayloadHash || receipt.Approval.ID != approval.ID ||
		receipt.Approval.ContentHash != approval.PayloadHash || receipt.Operation != input.Operation ||
		!sameOptionalExactReferences(receipt.SourceRevision, input.SourceRevision) ||
		receipt.Provider != strings.TrimSpace(input.Provider) || receipt.ProviderRef != strings.TrimSpace(input.ProviderRef) ||
		receipt.PublicURL != strings.TrimSpace(input.PublicURL) {
		return DeploymentRevision{}, invalid("deployment production receipt")
	}
	if (schemaVersion == DeploymentRevisionSchemaVersionV2) != (receipt.SchemaVersion == ProductionReceiptSchemaVersionV2) {
		return DeploymentRevision{}, invalid("deployment receipt authority generation")
	}
	if schemaVersion == DeploymentRevisionSchemaVersionV2 {
		if receipt.SchemaVersion != ProductionReceiptSchemaVersionV2 || receipt.ControllerOperation == nil ||
			*receipt.ControllerOperation != *controllerOperation {
			return DeploymentRevision{}, invalid("deployment controller operation result")
		}
	}
	if preview.ReleaseBundle.ID != bundle.ID || preview.ReleaseBundle.ContentHash != bundle.BundleHash ||
		!sameArtifacts(preview.ReleaseArtifacts, bundle.ReleaseArtifacts) {
		return DeploymentRevision{}, invalid("deployment artifact identity")
	}
	if !validUUID(input.ID) || !validUUID(input.RunID) || !validUUID(input.CreatedBy) || input.CreatedAt.IsZero() ||
		!boundedIdentifier(input.Provider, 128) || !boundedIdentifier(input.ProviderRef, 1000) ||
		!boundedIdentifier(input.PublicURL, 2000) {
		return DeploymentRevision{}, invalid("deployment revision identity")
	}
	checks, err := normalizePreviewChecks(input.Checks)
	if err != nil || !samePreviewChecks(checks, receipt.Checks) {
		return DeploymentRevision{}, invalid("deployment health checks")
	}
	for _, check := range checks {
		if check.Status != "passed" {
			return DeploymentRevision{}, invalid("deployment health decision")
		}
	}
	if input.Operation == DeploymentPromote && input.SourceRevision != nil {
		return DeploymentRevision{}, invalid("promotion source revision")
	}
	if input.Operation == DeploymentRollback {
		if input.SourceRevision == nil || !validUUID(input.SourceRevision.ID) || !exactHash(input.SourceRevision.ContentHash) {
			return DeploymentRevision{}, invalid("rollback source revision")
		}
	} else if input.Operation != DeploymentPromote {
		return DeploymentRevision{}, invalid("deployment operation")
	}
	previewRef, _ := preview.PassedReference()
	productionRef, _ := receipt.PassedReference()
	revision := DeploymentRevision{
		SchemaVersion: schemaVersion, ID: input.ID, RunID: input.RunID,
		ProjectID: bundle.ProjectID, ReleaseBundle: repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		PreviewReceipt:    previewRef,
		Approval:          repository.ExactReference{ID: approval.ID, ContentHash: approval.PayloadHash},
		ProductionReceipt: productionRef,
		Operation:         input.Operation, SourceRevision: cloneExactReference(input.SourceRevision),
		Provider: strings.TrimSpace(input.Provider), ProviderRef: strings.TrimSpace(input.ProviderRef),
		PublicURL: strings.TrimSpace(input.PublicURL), Checks: checks,
		ControllerOperation: cloneControllerOperationResultReference(controllerOperation), CreatedBy: input.CreatedBy,
		CreatedAt: input.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	hash, err := domain.CanonicalHash(deploymentRevisionHashPayload(revision))
	if err != nil {
		return DeploymentRevision{}, invalid("deployment revision payload")
	}
	revision.PayloadHash = "sha256:" + hash
	return revision, nil
}

func ParseDeploymentRevision(value DeploymentRevision) (DeploymentRevision, error) {
	if (value.SchemaVersion != DeploymentRevisionSchemaVersion && value.SchemaVersion != DeploymentRevisionSchemaVersionV2) ||
		validateControllerOperationResultReference(value.ControllerOperation, value.SchemaVersion == DeploymentRevisionSchemaVersionV2) != nil ||
		!validUUID(value.ID) ||
		!validUUID(value.RunID) || !validUUID(value.ProjectID) || !validUUID(value.CreatedBy) ||
		value.CreatedAt.IsZero() || !validUUID(value.ReleaseBundle.ID) || !exactHash(value.ReleaseBundle.ContentHash) ||
		!validUUID(value.PreviewReceipt.ID) || !exactHash(value.PreviewReceipt.ContentHash) ||
		!validUUID(value.Approval.ID) || !exactHash(value.Approval.ContentHash) ||
		!validUUID(value.ProductionReceipt.ID) || !exactHash(value.ProductionReceipt.ContentHash) ||
		!boundedIdentifier(value.Provider, 128) || !boundedIdentifier(value.ProviderRef, 1000) ||
		!boundedIdentifier(value.PublicURL, 2000) || !exactHash(value.PayloadHash) {
		return DeploymentRevision{}, invalid("deployment revision envelope")
	}
	checks, err := normalizePreviewChecks(value.Checks)
	if err != nil || !samePreviewChecks(checks, value.Checks) {
		return DeploymentRevision{}, invalid("deployment revision health checks")
	}
	for _, check := range checks {
		if check.Status != "passed" {
			return DeploymentRevision{}, invalid("deployment revision health decision")
		}
	}
	if value.Operation == DeploymentPromote && value.SourceRevision != nil {
		return DeploymentRevision{}, invalid("deployment promotion source")
	}
	if value.Operation == DeploymentRollback {
		if value.SourceRevision == nil || !validUUID(value.SourceRevision.ID) || !exactHash(value.SourceRevision.ContentHash) {
			return DeploymentRevision{}, invalid("deployment rollback source")
		}
	} else if value.Operation != DeploymentPromote {
		return DeploymentRevision{}, invalid("deployment revision operation")
	}
	hash, err := domain.CanonicalHash(deploymentRevisionHashPayload(value))
	if err != nil || value.PayloadHash != "sha256:"+hash {
		return DeploymentRevision{}, invalid("deployment revision hash")
	}
	value.CreatedAt = value.CreatedAt.UTC().Truncate(time.Microsecond)
	return value, nil
}

func (revision DeploymentRevision) ExactReference() (repository.ExactReference, error) {
	parsed, err := ParseDeploymentRevision(revision)
	if err != nil {
		return repository.ExactReference{}, err
	}
	return repository.ExactReference{ID: parsed.ID, ContentHash: parsed.PayloadHash}, nil
}

func normalizePreviewChecks(checks []PreviewCheck) ([]PreviewCheck, error) {
	if len(checks) == 0 || len(checks) > 128 {
		return nil, invalid("preview checks")
	}
	result := append([]PreviewCheck(nil), checks...)
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	for index := range result {
		result[index].ID = strings.TrimSpace(result[index].ID)
		result[index].Kind = strings.TrimSpace(result[index].Kind)
		result[index].Status = strings.TrimSpace(result[index].Status)
		result[index].Detail = strings.TrimSpace(result[index].Detail)
		if !boundedIdentifier(result[index].ID, 128) || !boundedIdentifier(result[index].Kind, 128) ||
			(result[index].Status != "passed" && result[index].Status != "failed") ||
			len(result[index].Detail) > 2000 || (index > 0 && result[index-1].ID == result[index].ID) {
			return nil, invalid("preview check")
		}
	}
	return result, nil
}

func samePreviewChecks(left, right []PreviewCheck) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validDeploymentArtifacts(artifacts []verification.CanonicalReleaseArtifact) bool {
	if len(artifacts) == 0 || len(artifacts) > 256 {
		return false
	}
	for index, artifact := range artifacts {
		if !boundedIdentifier(artifact.ID, 128) || !boundedIdentifier(artifact.Kind, 128) ||
			!boundedIdentifier(artifact.Store, 128) || !boundedIdentifier(artifact.Ref, 2000) ||
			!exactHash(artifact.ContentHash) || !boundedIdentifier(artifact.MediaType, 256) ||
			artifact.ByteSize < 0 || (index > 0 && artifacts[index-1].ID >= artifact.ID) {
			return false
		}
	}
	return true
}

func previewReceiptHashPayload(value PreviewReceipt) any {
	copy := value
	copy.PayloadHash = ""
	return copy
}

func promotionApprovalHashPayload(value PromotionApproval) any {
	copy := value
	copy.PayloadHash = ""
	return copy
}

func productionReceiptHashPayload(value ProductionReceipt) any {
	copy := value
	copy.PayloadHash = ""
	return copy
}

func deploymentRevisionHashPayload(value DeploymentRevision) any {
	copy := value
	copy.PayloadHash = ""
	return copy
}

func boundedIdentifier(value string, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(trimmed) <= maximum && !strings.ContainsRune(trimmed, '\x00')
}

func boundedText(value string, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(trimmed) <= maximum && !strings.ContainsRune(trimmed, '\x00')
}

func cloneExactReference(value *repository.ExactReference) *repository.ExactReference {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validateControllerOperationResultReference(
	reference *ControllerOperationResultReference,
	required bool,
) error {
	if !required {
		if reference != nil {
			return invalid("legacy delivery evidence cannot carry a v3 controller result")
		}
		return nil
	}
	if reference == nil || !validUUID(reference.OperationID) || !exactHash(reference.ResultHash) {
		return invalid("exact controller operation result")
	}
	return nil
}

func cloneControllerOperationResultReference(
	reference *ControllerOperationResultReference,
) *ControllerOperationResultReference {
	if reference == nil {
		return nil
	}
	copy := *reference
	return &copy
}

func sameOptionalExactReferences(left, right *repository.ExactReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func previewChecksPassed(checks []PreviewCheck) bool {
	for _, check := range checks {
		if check.Status != "passed" {
			return false
		}
	}
	return true
}

func checksCoverKinds(checks []PreviewCheck, requiredKinds ...string) bool {
	covered := make(map[string]bool, len(requiredKinds))
	for _, check := range checks {
		if check.Status == "passed" {
			covered[check.Kind] = true
		}
	}
	for _, kind := range requiredKinds {
		if !covered[kind] {
			return false
		}
	}
	return true
}

func validateDeploymentOperationSource(
	operation DeploymentOperation,
	source *repository.ExactReference,
) error {
	switch operation {
	case DeploymentPromote:
		if source != nil {
			return invalid("promotion source revision")
		}
	case DeploymentRollback:
		if source == nil || !validUUID(source.ID) || !exactHash(source.ContentHash) {
			return invalid("rollback source revision")
		}
	default:
		return invalid("deployment operation")
	}
	return nil
}
