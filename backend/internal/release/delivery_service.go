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
	"github.com/worksflow/builder/backend/internal/repository"
)

type DeliveryRunState string

const (
	DeliveryQueued           DeliveryRunState = "queued"
	DeliveryClaimed          DeliveryRunState = "claimed"
	DeliverySubmitting       DeliveryRunState = "submitting"
	DeliveryReconcileWait    DeliveryRunState = "reconcile_wait"
	DeliveryReconciling      DeliveryRunState = "reconciling"
	DeliveryVerifying        DeliveryRunState = "verifying"
	DeliveryReconcileBlocked DeliveryRunState = "reconcile_blocked"
	DeliveryPassed           DeliveryRunState = "passed"
	DeliveryHealthy          DeliveryRunState = "healthy"
	DeliveryFailed           DeliveryRunState = "failed"
	DeliveryError            DeliveryRunState = "error"
	DeliveryCancelled        DeliveryRunState = "cancelled"

	// DeliveryDeploying is retained only so historical v1 code and records can
	// be decoded. New work uses submitting/reconciling and never enters it.
	DeliveryDeploying DeliveryRunState = "deploying"
)

type PreviewRun struct {
	ID            string                     `json:"id"`
	ProjectID     string                     `json:"projectId"`
	ReleaseBundle repository.ExactReference  `json:"releaseBundle"`
	Reason        string                     `json:"reason"`
	State         DeliveryRunState           `json:"state"`
	Version       uint64                     `json:"version"`
	CreatedBy     string                     `json:"createdBy"`
	CreatedAt     time.Time                  `json:"createdAt"`
	UpdatedAt     time.Time                  `json:"updatedAt"`
	Receipt       *repository.ExactReference `json:"receipt,omitempty"`
}

type ProductionRun struct {
	ID                string                     `json:"id"`
	ProjectID         string                     `json:"projectId"`
	Environment       string                     `json:"environment"`
	Operation         DeploymentOperation        `json:"operation"`
	ReleaseBundle     repository.ExactReference  `json:"releaseBundle"`
	PreviewReceipt    repository.ExactReference  `json:"previewReceipt"`
	PromotionApproval repository.ExactReference  `json:"promotionApproval"`
	SourceRevision    *repository.ExactReference `json:"sourceRevision,omitempty"`
	ExpectedRevision  *repository.ExactReference `json:"expectedRevision,omitempty"`
	ExpectedReceipt   *repository.ExactReference `json:"expectedProductionReceipt,omitempty"`
	Reason            string                     `json:"reason"`
	State             DeliveryRunState           `json:"state"`
	Version           uint64                     `json:"version"`
	CreatedBy         string                     `json:"createdBy"`
	CreatedAt         time.Time                  `json:"createdAt"`
	UpdatedAt         time.Time                  `json:"updatedAt"`
	Receipt           *repository.ExactReference `json:"receipt,omitempty"`
	Revision          *repository.ExactReference `json:"revision,omitempty"`
}

type CreatePreviewRunInput struct {
	ID             string
	ProjectID      string
	ReleaseBundle  repository.ExactReference
	BundleDocument Bundle
	RequestKey     string
	RequestHash    string
	Reason         string
	CreatedBy      string
}

type CreateProductionRunInput struct {
	ID                string
	ProjectID         string
	Environment       string
	Operation         DeploymentOperation
	ReleaseBundle     repository.ExactReference
	PreviewReceipt    repository.ExactReference
	PromotionApproval repository.ExactReference
	SourceRevision    *repository.ExactReference
	BundleDocument    Bundle
	PreviewDocument   PreviewReceipt
	ApprovalDocument  PromotionApproval
	SourceDocument    *DeploymentRevision
	RequestKey        string
	RequestHash       string
	Reason            string
	CreatedBy         string
}

type DeliveryControlStore interface {
	Get(context.Context, string, string, string) (Bundle, error)
	CreatePreviewRun(context.Context, CreatePreviewRunInput) (PreviewRun, bool, error)
	GetPreviewRun(context.Context, string, string) (PreviewRun, error)
	ListPreviewRuns(context.Context, string, repository.ExactReference, int) ([]PreviewRun, error)
	GetPreviewReceipt(context.Context, string, string, string) (PreviewReceipt, error)
	SavePromotionApproval(context.Context, PromotionApproval) (PromotionApproval, bool, error)
	GetPromotionApproval(context.Context, string, string, string) (PromotionApproval, error)
	GetPromotionApprovalByPreview(context.Context, string, string, string) (PromotionApproval, error)
	CreateProductionRun(context.Context, CreateProductionRunInput) (ProductionRun, bool, error)
	GetProductionRun(context.Context, string, string) (ProductionRun, error)
	ListProductionRuns(context.Context, string, repository.ExactReference, int) ([]ProductionRun, error)
	ListProductionRunsForProject(context.Context, string, int) ([]ProductionRun, error)
	GetProductionReceipt(context.Context, string, string, string) (ProductionReceipt, error)
	GetDeploymentRevision(context.Context, string, string, string) (DeploymentRevision, error)
}

type DeliveryAuthorizer interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type DeliveryService struct {
	store  DeliveryControlStore
	access DeliveryAuthorizer
	now    func() time.Time
}

func NewDeliveryService(store DeliveryControlStore, access DeliveryAuthorizer) (*DeliveryService, error) {
	if store == nil || access == nil {
		return nil, errors.New("release delivery store and authorizer are required")
	}
	return &DeliveryService{store: store, access: access, now: time.Now}, nil
}

type StartPreviewRequest struct {
	ProjectID         string `json:"projectId"`
	ReleaseBundleID   string `json:"releaseBundleId"`
	ReleaseBundleHash string `json:"releaseBundleHash"`
	Reason            string `json:"reason"`
	ActorID           string `json:"-"`
	OperationID       string `json:"-"`
}

func (service *DeliveryService) StartPreview(ctx context.Context, request StartPreviewRequest) (PreviewRun, bool, error) {
	if err := validateDeliveryMutationIdentity(
		request.ProjectID, request.ReleaseBundleID, request.ReleaseBundleHash,
		request.ActorID, request.OperationID, request.Reason,
	); err != nil {
		return PreviewRun{}, false, err
	}
	if _, err := service.access.Authorize(ctx, request.ProjectID, request.ActorID, core.ActionEdit); err != nil {
		return PreviewRun{}, false, fmt.Errorf("authorize release preview: %w", err)
	}
	bundle, err := service.store.Get(ctx, request.ProjectID, request.ReleaseBundleID, request.ReleaseBundleHash)
	if err != nil {
		return PreviewRun{}, false, err
	}
	requestHash, err := deliveryRequestHash(map[string]any{
		"operation": "preview", "projectId": request.ProjectID,
		"releaseBundle": repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		"reason":        strings.TrimSpace(request.Reason),
	})
	if err != nil {
		return PreviewRun{}, false, err
	}
	runID := deterministicDeliveryID("release-preview", request.ProjectID, request.OperationID)
	return service.store.CreatePreviewRun(ctx, CreatePreviewRunInput{
		ID: runID, ProjectID: request.ProjectID,
		ReleaseBundle:  repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		BundleDocument: bundle,
		RequestKey:     strings.TrimSpace(request.OperationID), RequestHash: requestHash,
		Reason: strings.TrimSpace(request.Reason), CreatedBy: request.ActorID,
	})
}

type ApprovePromotionRequest struct {
	ProjectID          string `json:"projectId"`
	PreviewReceiptID   string `json:"previewReceiptId"`
	PreviewReceiptHash string `json:"previewReceiptHash"`
	Reason             string `json:"reason"`
	ActorID            string `json:"-"`
	OperationID        string `json:"-"`
}

func (service *DeliveryService) ApprovePromotion(
	ctx context.Context,
	request ApprovePromotionRequest,
) (PromotionApproval, bool, error) {
	if err := validateDeliveryMutationIdentity(
		request.ProjectID, request.PreviewReceiptID, request.PreviewReceiptHash,
		request.ActorID, request.OperationID, request.Reason,
	); err != nil {
		return PromotionApproval{}, false, err
	}
	if _, err := service.access.Authorize(ctx, request.ProjectID, request.ActorID, core.ActionPublish); err != nil {
		return PromotionApproval{}, false, fmt.Errorf("authorize production promotion: %w", err)
	}
	preview, err := service.store.GetPreviewReceipt(
		ctx, request.ProjectID, request.PreviewReceiptID, request.PreviewReceiptHash,
	)
	if err != nil {
		return PromotionApproval{}, false, err
	}
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(
			"release-promotion-approval\x00"+request.ProjectID+"\x00"+request.PreviewReceiptID+"\x00"+request.PreviewReceiptHash,
		)).String(),
		Preview: preview, Reason: request.Reason, CreatedBy: request.ActorID, CreatedAt: service.now().UTC(),
	})
	if err != nil {
		return PromotionApproval{}, false, err
	}
	return service.store.SavePromotionApproval(ctx, approval)
}

type StartPromotionRequest struct {
	ProjectID             string `json:"projectId"`
	PromotionApprovalID   string `json:"promotionApprovalId"`
	PromotionApprovalHash string `json:"promotionApprovalHash"`
	Reason                string `json:"reason"`
	ActorID               string `json:"-"`
	OperationID           string `json:"-"`
}

func (service *DeliveryService) StartPromotion(
	ctx context.Context,
	request StartPromotionRequest,
) (ProductionRun, bool, error) {
	if err := validateDeliveryMutationIdentity(
		request.ProjectID, request.PromotionApprovalID, request.PromotionApprovalHash,
		request.ActorID, request.OperationID, request.Reason,
	); err != nil {
		return ProductionRun{}, false, err
	}
	if _, err := service.access.Authorize(ctx, request.ProjectID, request.ActorID, core.ActionPublish); err != nil {
		return ProductionRun{}, false, fmt.Errorf("authorize production promotion: %w", err)
	}
	approval, err := service.store.GetPromotionApproval(
		ctx, request.ProjectID, request.PromotionApprovalID, request.PromotionApprovalHash,
	)
	if err != nil {
		return ProductionRun{}, false, err
	}
	preview, err := service.store.GetPreviewReceipt(
		ctx, request.ProjectID, approval.PreviewReceipt.ID, approval.PreviewReceipt.ContentHash,
	)
	if err != nil {
		return ProductionRun{}, false, err
	}
	bundle, err := service.store.Get(
		ctx, request.ProjectID, approval.ReleaseBundle.ID, approval.ReleaseBundle.ContentHash,
	)
	if err != nil {
		return ProductionRun{}, false, err
	}
	requestHash, err := deliveryRequestHash(map[string]any{
		"operation": "promote", "projectId": request.ProjectID,
		"environment":   "production",
		"approval":      repository.ExactReference{ID: approval.ID, ContentHash: approval.PayloadHash},
		"releaseBundle": approval.ReleaseBundle, "previewReceipt": approval.PreviewReceipt,
		"reason": strings.TrimSpace(request.Reason),
	})
	if err != nil {
		return ProductionRun{}, false, err
	}
	return service.store.CreateProductionRun(ctx, CreateProductionRunInput{
		ID:        deterministicDeliveryID("release-promotion", request.ProjectID, request.OperationID),
		ProjectID: request.ProjectID, Environment: "production", Operation: DeploymentPromote,
		ReleaseBundle: approval.ReleaseBundle, PreviewReceipt: approval.PreviewReceipt,
		PromotionApproval: repository.ExactReference{ID: approval.ID, ContentHash: approval.PayloadHash},
		BundleDocument:    bundle, PreviewDocument: preview, ApprovalDocument: approval,
		RequestKey: strings.TrimSpace(request.OperationID), RequestHash: requestHash,
		Reason: strings.TrimSpace(request.Reason), CreatedBy: request.ActorID,
	})
}

type StartRollbackRequest struct {
	ProjectID          string `json:"projectId"`
	SourceRevisionID   string `json:"sourceRevisionId"`
	SourceRevisionHash string `json:"sourceRevisionHash"`
	Reason             string `json:"reason"`
	ActorID            string `json:"-"`
	OperationID        string `json:"-"`
}

func (service *DeliveryService) StartRollback(
	ctx context.Context,
	request StartRollbackRequest,
) (ProductionRun, bool, error) {
	if err := validateDeliveryMutationIdentity(
		request.ProjectID, request.SourceRevisionID, request.SourceRevisionHash,
		request.ActorID, request.OperationID, request.Reason,
	); err != nil {
		return ProductionRun{}, false, err
	}
	if _, err := service.access.Authorize(ctx, request.ProjectID, request.ActorID, core.ActionPublish); err != nil {
		return ProductionRun{}, false, fmt.Errorf("authorize production rollback: %w", err)
	}
	source, err := service.store.GetDeploymentRevision(
		ctx, request.ProjectID, request.SourceRevisionID, request.SourceRevisionHash,
	)
	if err != nil {
		return ProductionRun{}, false, err
	}
	sourceRef, _ := source.ExactReference()
	bundle, err := service.store.Get(
		ctx, request.ProjectID, source.ReleaseBundle.ID, source.ReleaseBundle.ContentHash,
	)
	if err != nil {
		return ProductionRun{}, false, err
	}
	preview, err := service.store.GetPreviewReceipt(
		ctx, request.ProjectID, source.PreviewReceipt.ID, source.PreviewReceipt.ContentHash,
	)
	if err != nil {
		return ProductionRun{}, false, err
	}
	approval, err := service.store.GetPromotionApproval(
		ctx, request.ProjectID, source.Approval.ID, source.Approval.ContentHash,
	)
	if err != nil {
		return ProductionRun{}, false, err
	}
	requestHash, err := deliveryRequestHash(map[string]any{
		"operation": "rollback", "projectId": request.ProjectID, "sourceRevision": sourceRef,
		"environment":   "production",
		"releaseBundle": source.ReleaseBundle, "previewReceipt": source.PreviewReceipt,
		"promotionApproval": source.Approval, "reason": strings.TrimSpace(request.Reason),
	})
	if err != nil {
		return ProductionRun{}, false, err
	}
	return service.store.CreateProductionRun(ctx, CreateProductionRunInput{
		ID:        deterministicDeliveryID("release-rollback", request.ProjectID, request.OperationID),
		ProjectID: request.ProjectID, Environment: "production", Operation: DeploymentRollback,
		ReleaseBundle: source.ReleaseBundle, PreviewReceipt: source.PreviewReceipt,
		PromotionApproval: source.Approval, SourceRevision: &sourceRef,
		BundleDocument: bundle, PreviewDocument: preview, ApprovalDocument: approval,
		SourceDocument: &source,
		RequestKey:     strings.TrimSpace(request.OperationID), RequestHash: requestHash,
		Reason: strings.TrimSpace(request.Reason), CreatedBy: request.ActorID,
	})
}

func (service *DeliveryService) GetPreviewRun(
	ctx context.Context,
	projectID, runID, actorID string,
) (PreviewRun, error) {
	if !validUUID(projectID) || !validUUID(runID) || !validUUID(actorID) {
		return PreviewRun{}, invalid("get preview run request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return PreviewRun{}, err
	}
	return service.store.GetPreviewRun(ctx, projectID, runID)
}

func (service *DeliveryService) ListPreviewRuns(
	ctx context.Context,
	projectID, bundleID, bundleHash, actorID string,
) ([]PreviewRun, error) {
	if !validUUID(projectID) || !validUUID(bundleID) || !exactHash(bundleHash) || !validUUID(actorID) {
		return nil, invalid("list preview runs request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	return service.store.ListPreviewRuns(ctx, projectID, repository.ExactReference{ID: bundleID, ContentHash: bundleHash}, 50)
}

func (service *DeliveryService) GetPreviewReceipt(
	ctx context.Context,
	projectID, receiptID, receiptHash, actorID string,
) (PreviewReceipt, error) {
	if !validUUID(projectID) || !validUUID(receiptID) || !exactHash(receiptHash) || !validUUID(actorID) {
		return PreviewReceipt{}, invalid("get preview receipt request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return PreviewReceipt{}, err
	}
	return service.store.GetPreviewReceipt(ctx, projectID, receiptID, receiptHash)
}

func (service *DeliveryService) GetPromotionApprovalByPreview(
	ctx context.Context,
	projectID, previewID, previewHash, actorID string,
) (PromotionApproval, error) {
	if !validUUID(projectID) || !validUUID(previewID) || !exactHash(previewHash) || !validUUID(actorID) {
		return PromotionApproval{}, invalid("get promotion approval by preview request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return PromotionApproval{}, err
	}
	return service.store.GetPromotionApprovalByPreview(ctx, projectID, previewID, previewHash)
}

func (service *DeliveryService) GetProductionRun(
	ctx context.Context,
	projectID, runID, actorID string,
) (ProductionRun, error) {
	if !validUUID(projectID) || !validUUID(runID) || !validUUID(actorID) {
		return ProductionRun{}, invalid("get production run request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return ProductionRun{}, err
	}
	return service.store.GetProductionRun(ctx, projectID, runID)
}

func (service *DeliveryService) ListProductionRuns(
	ctx context.Context,
	projectID, bundleID, bundleHash, actorID string,
) ([]ProductionRun, error) {
	if !validUUID(projectID) || !validUUID(bundleID) || !exactHash(bundleHash) || !validUUID(actorID) {
		return nil, invalid("list production runs request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	return service.store.ListProductionRuns(ctx, projectID, repository.ExactReference{ID: bundleID, ContentHash: bundleHash}, 50)
}

func (service *DeliveryService) ListProductionHistory(
	ctx context.Context,
	projectID, actorID string,
) ([]ProductionRun, error) {
	if !validUUID(projectID) || !validUUID(actorID) {
		return nil, invalid("list production history request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	return service.store.ListProductionRunsForProject(ctx, projectID, 100)
}

func (service *DeliveryService) GetProductionReceipt(
	ctx context.Context,
	projectID, receiptID, receiptHash, actorID string,
) (ProductionReceipt, error) {
	if !validUUID(projectID) || !validUUID(receiptID) || !exactHash(receiptHash) || !validUUID(actorID) {
		return ProductionReceipt{}, invalid("get production receipt request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return ProductionReceipt{}, err
	}
	return service.store.GetProductionReceipt(ctx, projectID, receiptID, receiptHash)
}

func (service *DeliveryService) GetDeploymentRevision(
	ctx context.Context,
	projectID, revisionID, revisionHash, actorID string,
) (DeploymentRevision, error) {
	if !validUUID(projectID) || !validUUID(revisionID) || !exactHash(revisionHash) || !validUUID(actorID) {
		return DeploymentRevision{}, invalid("get deployment revision request")
	}
	if _, err := service.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return DeploymentRevision{}, err
	}
	return service.store.GetDeploymentRevision(ctx, projectID, revisionID, revisionHash)
}

func validateDeliveryMutationIdentity(
	projectID, exactID, exactContentHash, actorID, operationID, reason string,
) error {
	if !validUUID(strings.TrimSpace(projectID)) || !validUUID(strings.TrimSpace(exactID)) ||
		!exactHash(strings.TrimSpace(exactContentHash)) || !validUUID(strings.TrimSpace(actorID)) ||
		!boundedIdentifier(operationID, 128) || !boundedText(reason, 1000) {
		return invalid("release delivery mutation request")
	}
	return nil
}

func deliveryRequestHash(value any) (string, error) {
	hash, err := domain.CanonicalHash(value)
	if err != nil {
		return "", invalid("release delivery request hash")
	}
	return "sha256:" + hash, nil
}

func deterministicDeliveryID(kind, projectID, operationID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(kind+"\x00"+projectID+"\x00"+strings.TrimSpace(operationID))).String()
}
