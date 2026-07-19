package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	"github.com/worksflow/builder/backend/internal/release"
)

type ReleaseAPI interface {
	CreateBundle(context.Context, release.CreateBundleRequest) (release.BundleView, error)
	GetBundle(context.Context, string, string, string, string) (release.Bundle, error)
	GetBundleByReceipt(context.Context, string, string, string, string) (release.Bundle, error)
}

type ReleaseDependencies struct {
	Service ReleaseAPI
	// Delivery is retained as a convenience for callers that enable both the
	// immutable read surface and mutations. Production wiring uses the split
	// fields so controller maintenance cannot hide historical evidence.
	Delivery         ReleaseDeliveryAPI
	DeliveryRead     ReleaseDeliveryReadAPI
	DeliveryMutation ReleaseDeliveryMutationAPI
	MaxJSONBodyBytes int64
}

type ReleaseDeliveryMutationAPI interface {
	StartPreview(context.Context, release.StartPreviewRequest) (release.PreviewRun, bool, error)
	ApprovePromotion(context.Context, release.ApprovePromotionRequest) (release.PromotionApproval, bool, error)
	StartPromotion(context.Context, release.StartPromotionRequest) (release.ProductionRun, bool, error)
	StartRollback(context.Context, release.StartRollbackRequest) (release.ProductionRun, bool, error)
}

type ReleaseDeliveryReadAPI interface {
	GetPreviewRun(context.Context, string, string, string) (release.PreviewRun, error)
	ListPreviewRuns(context.Context, string, string, string, string) ([]release.PreviewRun, error)
	GetPreviewReceipt(context.Context, string, string, string, string) (release.PreviewReceipt, error)
	GetPromotionApprovalByPreview(context.Context, string, string, string, string) (release.PromotionApproval, error)
	GetProductionRun(context.Context, string, string, string) (release.ProductionRun, error)
	ListProductionRuns(context.Context, string, string, string, string) ([]release.ProductionRun, error)
	ListProductionHistory(context.Context, string, string) ([]release.ProductionRun, error)
	GetProductionReceipt(context.Context, string, string, string, string) (release.ProductionReceipt, error)
	GetDeploymentRevision(context.Context, string, string, string, string) (release.DeploymentRevision, error)
}

type ReleaseDeliveryReconciliationMutationAPI interface {
	ResumeBlockedDelivery(context.Context, release.ResumeBlockedDeliveryRequest) (release.DeliveryReconciliationCase, bool, error)
}

type ReleaseDeliveryReconciliationReadAPI interface {
	GetDeliveryReconciliationCase(context.Context, string, string, string) (release.DeliveryReconciliationCase, error)
	ListDeliveryReconciliationCases(context.Context, string, string) ([]release.DeliveryReconciliationCase, error)
	GetBlockedDeliveryReconciliationSnapshot(context.Context, string, release.DeliveryOperationKind, string, string) (release.DeliveryReconciliationBlockSnapshot, error)
}

type ReleaseDeliveryAPI interface {
	ReleaseDeliveryMutationAPI
	ReleaseDeliveryReadAPI
}

type ReleaseHandler struct {
	service                ReleaseAPI
	deliveryRead           ReleaseDeliveryReadAPI
	deliveryMutation       ReleaseDeliveryMutationAPI
	reconciliationRead     ReleaseDeliveryReconciliationReadAPI
	reconciliationMutation ReleaseDeliveryReconciliationMutationAPI
	maxJSON                int64
}

func NewReleaseHandler(dependencies ReleaseDependencies) (*ReleaseHandler, error) {
	if dependencies.Service == nil {
		return nil, errors.New("release service is required")
	}
	maximum := dependencies.MaxJSONBodyBytes
	if maximum <= 0 {
		maximum = 1 << 20
	}
	readAPI := dependencies.DeliveryRead
	mutationAPI := dependencies.DeliveryMutation
	if dependencies.Delivery != nil {
		if readAPI == nil {
			readAPI = dependencies.Delivery
		}
		if mutationAPI == nil {
			mutationAPI = dependencies.Delivery
		}
	}
	reconciliationRead, _ := readAPI.(ReleaseDeliveryReconciliationReadAPI)
	reconciliationMutation, _ := mutationAPI.(ReleaseDeliveryReconciliationMutationAPI)
	return &ReleaseHandler{
		service: dependencies.Service, deliveryRead: readAPI,
		deliveryMutation: mutationAPI, reconciliationRead: reconciliationRead,
		reconciliationMutation: reconciliationMutation, maxJSON: maximum,
	}, nil
}

func RegisterReleaseRoutes(
	routes gin.IRoutes,
	handler *ReleaseHandler,
	mutationMiddleware ...gin.HandlerFunc,
) error {
	if routes == nil || handler == nil {
		return errors.New("release routes and handler are required")
	}
	mutation := []gin.HandlerFunc{releaseNoStore}
	mutation = append(mutation, mutationMiddleware...)
	mutation = append(mutation, handler.createBundle)
	routes.POST("/projects/:projectId/release-bundles", mutation...)
	routes.GET("/projects/:projectId/release-bundles/by-receipt", releaseNoStore, handler.getBundleByReceipt)
	routes.GET("/projects/:projectId/release-bundles/:bundleId", releaseNoStore, handler.getBundle)
	routes.GET("/projects/:projectId/release-capabilities", releaseNoStore, handler.getCapabilities)
	if handler.deliveryMutation != nil {
		routes.POST("/projects/:projectId/release-preview-runs", releaseMutationHandlers(mutationMiddleware, handler.startPreview)...)
		routes.POST("/projects/:projectId/release-promotion-approvals", releaseMutationHandlers(mutationMiddleware, handler.approvePromotion)...)
		routes.POST("/projects/:projectId/release-deployment-runs/promote", releaseMutationHandlers(mutationMiddleware, handler.startPromotion)...)
		routes.POST("/projects/:projectId/release-deployment-runs/rollback", releaseMutationHandlers(mutationMiddleware, handler.startRollback)...)
	}
	if handler.reconciliationMutation != nil {
		routes.POST("/projects/:projectId/release-delivery-reconciliation-cases", releaseMutationHandlers(mutationMiddleware, handler.resumeBlockedDelivery)...)
	}
	if handler.deliveryRead != nil {
		routes.GET("/projects/:projectId/release-preview-runs", releaseNoStore, handler.listPreviewRuns)
		routes.GET("/projects/:projectId/release-preview-runs/:runId", releaseNoStore, handler.getPreviewRun)
		routes.GET("/projects/:projectId/release-preview-receipts/:receiptId", releaseNoStore, handler.getPreviewReceipt)
		routes.GET("/projects/:projectId/release-promotion-approvals/by-preview", releaseNoStore, handler.getPromotionApprovalByPreview)
		routes.GET("/projects/:projectId/release-deployment-runs/:runId", releaseNoStore, handler.getProductionRun)
		routes.GET("/projects/:projectId/release-deployment-runs", releaseNoStore, handler.listProductionRuns)
		routes.GET("/projects/:projectId/release-production-receipts/:receiptId", releaseNoStore, handler.getProductionReceipt)
		routes.GET("/projects/:projectId/release-deployment-revisions/:revisionId", releaseNoStore, handler.getDeploymentRevision)
	}
	if handler.reconciliationRead != nil {
		routes.GET("/projects/:projectId/release-delivery-reconciliation-cases", releaseNoStore, handler.listDeliveryReconciliationCases)
		routes.GET("/projects/:projectId/release-delivery-reconciliation-cases/:caseId", releaseNoStore, handler.getDeliveryReconciliationCase)
		routes.GET("/projects/:projectId/release-delivery-reconciliation-blocks/:runKind/:runId", releaseNoStore, handler.getBlockedDeliveryReconciliationSnapshot)
	}
	return nil
}

func (handler *ReleaseHandler) getCapabilities(c *gin.Context) {
	if _, ok := actorID(c); !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"schemaVersion":   "release-capabilities/v1",
		"deliveryEnabled": handler.deliveryMutation != nil,
	})
}

func releaseMutationHandlers(middleware []gin.HandlerFunc, endpoint gin.HandlerFunc) []gin.HandlerFunc {
	result := make([]gin.HandlerFunc, 0, len(middleware)+2)
	result = append(result, releaseNoStore)
	result = append(result, middleware...)
	return append(result, endpoint)
}

func (handler *ReleaseHandler) getBundleByReceipt(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	bundle, err := handler.service.GetBundleByReceipt(
		c.Request.Context(), c.Param("projectId"), strings.TrimSpace(c.Query("receiptId")),
		strings.TrimSpace(c.Query("receiptHash")), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf("\"release-bundle:%s:%s\"", bundle.ID, bundle.BundleHash))
	c.JSON(http.StatusOK, bundle)
}

type createReleaseBundleRequest struct {
	CanonicalReceipt struct {
		ID          string `json:"id"`
		ContentHash string `json:"contentHash"`
	} `json:"canonicalReceipt"`
}

func (handler *ReleaseHandler) createBundle(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var input createReleaseBundleRequest
	if err := DecodeJSON(c, &input, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	view, err := handler.service.CreateBundle(c.Request.Context(), release.CreateBundleRequest{
		ProjectID: c.Param("projectId"), CanonicalReceiptID: input.CanonicalReceipt.ID,
		CanonicalReceiptHash: input.CanonicalReceipt.ContentHash, ActorID: actor,
		OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf("\"release-bundle:%s:%s\"", view.Bundle.ID, view.Bundle.BundleHash))
	c.Header("Location", "/v1/projects/"+view.Bundle.ProjectID+"/release-bundles/"+view.Bundle.ID+"?bundleHash="+view.Bundle.BundleHash)
	status := http.StatusCreated
	if view.Replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, view)
}

func (handler *ReleaseHandler) getBundle(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	bundleHash := strings.TrimSpace(c.Query("bundleHash"))
	bundle, err := handler.service.GetBundle(
		c.Request.Context(), c.Param("projectId"), c.Param("bundleId"), bundleHash, actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf("\"release-bundle:%s:%s\"", bundle.ID, bundle.BundleHash))
	c.JSON(http.StatusOK, bundle)
}

type exactReleaseReferenceRequest struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type startPreviewRequest struct {
	ReleaseBundle exactReleaseReferenceRequest `json:"releaseBundle"`
	Reason        string                       `json:"reason"`
}

func (handler *ReleaseHandler) startPreview(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var input startPreviewRequest
	if err := DecodeJSON(c, &input, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	run, replayed, err := handler.deliveryMutation.StartPreview(c.Request.Context(), release.StartPreviewRequest{
		ProjectID: c.Param("projectId"), ReleaseBundleID: input.ReleaseBundle.ID,
		ReleaseBundleHash: input.ReleaseBundle.ContentHash, Reason: input.Reason,
		ActorID: actor, OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	writePreviewRun(c, run, replayed)
}

func (handler *ReleaseHandler) getPreviewRun(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	run, err := handler.deliveryRead.GetPreviewRun(c.Request.Context(), c.Param("projectId"), c.Param("runId"), actor)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-preview-run:%s:%d"`, run.ID, run.Version))
	c.JSON(http.StatusOK, run)
}

func (handler *ReleaseHandler) listPreviewRuns(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	runs, err := handler.deliveryRead.ListPreviewRuns(
		c.Request.Context(), c.Param("projectId"), strings.TrimSpace(c.Query("bundleId")),
		strings.TrimSpace(c.Query("bundleHash")), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	if runs == nil {
		runs = []release.PreviewRun{}
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

func (handler *ReleaseHandler) getPreviewReceipt(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	receipt, err := handler.deliveryRead.GetPreviewReceipt(
		c.Request.Context(), c.Param("projectId"), c.Param("receiptId"),
		strings.TrimSpace(c.Query("receiptHash")), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-preview-receipt:%s:%s"`, receipt.ID, receipt.PayloadHash))
	c.JSON(http.StatusOK, receipt)
}

type approvePromotionRequest struct {
	PreviewReceipt exactReleaseReferenceRequest `json:"previewReceipt"`
	Reason         string                       `json:"reason"`
}

func (handler *ReleaseHandler) approvePromotion(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var input approvePromotionRequest
	if err := DecodeJSON(c, &input, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	approval, replayed, err := handler.deliveryMutation.ApprovePromotion(c.Request.Context(), release.ApprovePromotionRequest{
		ProjectID: c.Param("projectId"), PreviewReceiptID: input.PreviewReceipt.ID,
		PreviewReceiptHash: input.PreviewReceipt.ContentHash, Reason: input.Reason,
		ActorID: actor, OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-promotion-approval:%s:%s"`, approval.ID, approval.PayloadHash))
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, gin.H{"approval": approval, "replayed": replayed})
}

func (handler *ReleaseHandler) getPromotionApprovalByPreview(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	approval, err := handler.deliveryRead.GetPromotionApprovalByPreview(
		c.Request.Context(), c.Param("projectId"), strings.TrimSpace(c.Query("previewId")),
		strings.TrimSpace(c.Query("previewHash")), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-promotion-approval:%s:%s"`, approval.ID, approval.PayloadHash))
	c.JSON(http.StatusOK, approval)
}

type startPromotionRequest struct {
	PromotionApproval exactReleaseReferenceRequest `json:"promotionApproval"`
	Reason            string                       `json:"reason"`
}

func (handler *ReleaseHandler) startPromotion(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var input startPromotionRequest
	if err := DecodeJSON(c, &input, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	run, replayed, err := handler.deliveryMutation.StartPromotion(c.Request.Context(), release.StartPromotionRequest{
		ProjectID: c.Param("projectId"), PromotionApprovalID: input.PromotionApproval.ID,
		PromotionApprovalHash: input.PromotionApproval.ContentHash, Reason: input.Reason,
		ActorID: actor, OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	writeProductionRun(c, run, replayed)
}

type startRollbackRequest struct {
	SourceRevision exactReleaseReferenceRequest `json:"sourceRevision"`
	Reason         string                       `json:"reason"`
}

func (handler *ReleaseHandler) startRollback(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var input startRollbackRequest
	if err := DecodeJSON(c, &input, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	run, replayed, err := handler.deliveryMutation.StartRollback(c.Request.Context(), release.StartRollbackRequest{
		ProjectID: c.Param("projectId"), SourceRevisionID: input.SourceRevision.ID,
		SourceRevisionHash: input.SourceRevision.ContentHash, Reason: input.Reason,
		ActorID: actor, OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	writeProductionRun(c, run, replayed)
}

type resumeBlockedDeliveryRequest struct {
	RunKind           release.DeliveryOperationKind `json:"runKind"`
	RunID             string                        `json:"runId"`
	ExpectedVersion   uint64                        `json:"expectedVersion"`
	ExpectedErrorCode string                        `json:"expectedErrorCode"`
	Reason            string                        `json:"reason"`
}

func (handler *ReleaseHandler) resumeBlockedDelivery(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var input resumeBlockedDeliveryRequest
	if err := DecodeJSON(c, &input, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	resolution, replayed, err := handler.reconciliationMutation.ResumeBlockedDelivery(
		c.Request.Context(),
		release.ResumeBlockedDeliveryRequest{
			ProjectID: c.Param("projectId"), RunKind: input.RunKind, RunID: input.RunID,
			ExpectedVersion: input.ExpectedVersion, ExpectedErrorCode: input.ExpectedErrorCode,
			Reason: input.Reason, ActorID: actor,
			OperationID: worksmiddleware.IdempotencyKey(c),
		},
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-delivery-reconciliation-case:%s:%s"`, resolution.ID, resolution.CaseHash))
	c.Header("Location", "/v1/projects/"+resolution.ProjectID+"/release-delivery-reconciliation-cases/"+resolution.ID)
	status := http.StatusAccepted
	if replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, gin.H{"case": resolution, "replayed": replayed})
}

func (handler *ReleaseHandler) getDeliveryReconciliationCase(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	resolution, err := handler.reconciliationRead.GetDeliveryReconciliationCase(
		c.Request.Context(), c.Param("projectId"), c.Param("caseId"), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-delivery-reconciliation-case:%s:%s"`, resolution.ID, resolution.CaseHash))
	c.JSON(http.StatusOK, resolution)
}

func (handler *ReleaseHandler) listDeliveryReconciliationCases(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	cases, err := handler.reconciliationRead.ListDeliveryReconciliationCases(
		c.Request.Context(), c.Param("projectId"), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	if cases == nil {
		cases = []release.DeliveryReconciliationCase{}
	}
	c.JSON(http.StatusOK, gin.H{"cases": cases})
}

func (handler *ReleaseHandler) getBlockedDeliveryReconciliationSnapshot(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	snapshot, err := handler.reconciliationRead.GetBlockedDeliveryReconciliationSnapshot(
		c.Request.Context(), c.Param("projectId"), release.DeliveryOperationKind(c.Param("runKind")),
		c.Param("runId"), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-delivery-reconciliation-block:%s:%d"`, snapshot.RunID, snapshot.ExpectedRunVersion))
	c.JSON(http.StatusOK, snapshot)
}

func (handler *ReleaseHandler) getProductionRun(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	run, err := handler.deliveryRead.GetProductionRun(c.Request.Context(), c.Param("projectId"), c.Param("runId"), actor)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-deployment-run:%s:%d"`, run.ID, run.Version))
	c.JSON(http.StatusOK, run)
}

func (handler *ReleaseHandler) listProductionRuns(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	bundleID, bundleHash := strings.TrimSpace(c.Query("bundleId")), strings.TrimSpace(c.Query("bundleHash"))
	var runs []release.ProductionRun
	var err error
	if bundleID == "" && bundleHash == "" {
		runs, err = handler.deliveryRead.ListProductionHistory(c.Request.Context(), c.Param("projectId"), actor)
	} else {
		runs, err = handler.deliveryRead.ListProductionRuns(
			c.Request.Context(), c.Param("projectId"), bundleID, bundleHash, actor,
		)
	}
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	if runs == nil {
		runs = []release.ProductionRun{}
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

func (handler *ReleaseHandler) getProductionReceipt(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	receipt, err := handler.deliveryRead.GetProductionReceipt(
		c.Request.Context(), c.Param("projectId"), c.Param("receiptId"),
		strings.TrimSpace(c.Query("receiptHash")), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-production-receipt:%s:%s"`, receipt.ID, receipt.PayloadHash))
	c.JSON(http.StatusOK, receipt)
}

func (handler *ReleaseHandler) getDeploymentRevision(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	revision, err := handler.deliveryRead.GetDeploymentRevision(
		c.Request.Context(), c.Param("projectId"), c.Param("revisionId"),
		strings.TrimSpace(c.Query("revisionHash")), actor,
	)
	if err != nil {
		writeReleaseProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"release-deployment-revision:%s:%s"`, revision.ID, revision.PayloadHash))
	c.JSON(http.StatusOK, revision)
}

func writePreviewRun(c *gin.Context, run release.PreviewRun, replayed bool) {
	c.Header("ETag", fmt.Sprintf(`"release-preview-run:%s:%d"`, run.ID, run.Version))
	c.Header("Location", "/v1/projects/"+run.ProjectID+"/release-preview-runs/"+run.ID)
	status := http.StatusAccepted
	if replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, gin.H{"run": run, "replayed": replayed})
}

func writeProductionRun(c *gin.Context, run release.ProductionRun, replayed bool) {
	c.Header("ETag", fmt.Sprintf(`"release-deployment-run:%s:%d"`, run.ID, run.Version))
	c.Header("Location", "/v1/projects/"+run.ProjectID+"/release-deployment-runs/"+run.ID)
	status := http.StatusAccepted
	if replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, gin.H{"run": run, "replayed": replayed})
}

func releaseNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func writeReleaseProblem(c *gin.Context, err error) {
	switch {
	case errors.Is(err, release.ErrInvalidBundle):
		problem.Write(c, problem.New(
			http.StatusUnprocessableEntity, "invalid_release_bundle",
			"ReleaseBundle request is invalid", "One or more exact ReleaseBundle values are invalid.",
		))
	case errors.Is(err, release.ErrBundleNotFound):
		problem.Write(c, problem.New(
			http.StatusNotFound, "release_bundle_not_found",
			"ReleaseBundle not found", "The exact immutable ReleaseBundle was not found.",
		))
	case errors.Is(err, release.ErrBundleConflict):
		problem.Write(c, problem.New(
			http.StatusConflict, "release_bundle_conflict",
			"ReleaseBundle conflicts", "A different immutable Bundle already owns this exact release lineage.",
		))
	case errors.Is(err, release.ErrPreviewRunConflict):
		problem.Write(c, problem.New(
			http.StatusConflict, "release_preview_run_conflict",
			"Preview deployment conflicts", "The exact ReleaseBundle already has an active or unresolved Preview run. Reuse that run or wait for an explicit terminal decision.",
		))
	case errors.Is(err, release.ErrProductionHeadConflict):
		problem.Write(c, problem.New(
			http.StatusConflict, "release_production_head_conflict",
			"Production deployment conflicts", "The production environment head changed or another deployment is still active. Refresh production history before retrying.",
		))
	case errors.Is(err, release.ErrDeliveryReconciliationNotFound):
		problem.Write(c, problem.New(
			http.StatusNotFound, "release_delivery_reconciliation_not_found",
			"Delivery reconciliation case not found", "The immutable reconciliation audit case was not found.",
		))
	case errors.Is(err, release.ErrDeliveryReconciliationLegacy):
		problem.Write(c, problem.New(
			http.StatusConflict, "legacy_release_delivery_not_recoverable",
			"Historical delivery run cannot be resumed", "A v1 run has no exact controller Operation and remains historical blocked evidence.",
		))
	case errors.Is(err, release.ErrDeliveryReconciliationConflict):
		problem.Write(c, problem.New(
			http.StatusConflict, "release_delivery_reconciliation_conflict",
			"Delivery reconciliation state changed", "Refresh the blocked run and its exact quarantine error before retrying with a new idempotency key.",
		))
	case errors.Is(err, release.ErrDeliveryReconciliationDisabled):
		problem.Write(c, problem.New(
			http.StatusServiceUnavailable, "release_delivery_reconciliation_unavailable",
			"Delivery reconciliation is unavailable", "The governed reconciliation authority is not enabled on this server.",
		))
	case errors.Is(err, release.ErrBundleIntegrity):
		problem.Write(c, problem.New(
			http.StatusInternalServerError, "release_bundle_integrity",
			"ReleaseBundle integrity check failed", "Stored Bundle facts do not match their immutable content.",
		))
	default:
		writeBusinessProblem(c, err)
	}
}
