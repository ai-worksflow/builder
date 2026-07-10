package transport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/delivery"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

const maxDeliveryRequestBytes int64 = 256 << 10

type DeliveryQualityAPI interface {
	Evaluate(context.Context, string, string, delivery.QualityRunInput) (delivery.QualityReport, error)
	Get(context.Context, string, string) (delivery.QualityReport, error)
	List(context.Context, string, string, string) ([]delivery.QualityReport, error)
}

type DeliveryExportAPI interface {
	Export(context.Context, string, string, delivery.ExportInput) (delivery.Archive, error)
}

type DeliveryPublishAPI interface {
	Publish(context.Context, string, string, string, delivery.PublishInput) (delivery.Deployment, error)
	Rollback(context.Context, string, string, string, delivery.RollbackInput) (delivery.Deployment, error)
	Get(context.Context, string, string) (delivery.Deployment, error)
	List(context.Context, string, string) ([]delivery.Deployment, error)
	Logs(context.Context, string, string) ([]delivery.DeploymentLog, error)
}

type DeliveryDependencies struct {
	Quality          DeliveryQualityAPI
	Export           DeliveryExportAPI
	Publish          DeliveryPublishAPI
	StaticAssets     delivery.StaticAssetServer
	MaxJSONBodyBytes int64
}

type DeliveryHandler struct {
	quality      DeliveryQualityAPI
	export       DeliveryExportAPI
	publish      DeliveryPublishAPI
	staticAssets delivery.StaticAssetServer
	maxBody      int64
}

func NewDeliveryHandler(dependencies DeliveryDependencies) (*DeliveryHandler, error) {
	if dependencies.Quality == nil || dependencies.Export == nil || dependencies.Publish == nil {
		return nil, errors.New("delivery quality, export, and publish services are required")
	}
	if dependencies.MaxJSONBodyBytes <= 0 || dependencies.MaxJSONBodyBytes > maxDeliveryRequestBytes {
		dependencies.MaxJSONBodyBytes = maxDeliveryRequestBytes
	}
	return &DeliveryHandler{
		quality: dependencies.Quality, export: dependencies.Export, publish: dependencies.Publish,
		staticAssets: dependencies.StaticAssets, maxBody: dependencies.MaxJSONBodyBytes,
	}, nil
}

// RegisterDeliveryRoutes is independent from the root router. The caller
// passes an authenticated /v1 group plus CSRF, idempotency capture, and durable
// idempotency middleware in execution order. If-Match is enforced by the
// handler because first publish is a create while later publishes are updates.
func RegisterDeliveryRoutes(routes gin.IRoutes, handler *DeliveryHandler, mutationMiddleware ...gin.HandlerFunc) error {
	if routes == nil || handler == nil {
		return errors.New("delivery routes and handler are required")
	}
	routes.POST("/projects/:projectId/quality-runs", deliveryHandlers(mutationMiddleware, handler.createQualityRun)...)
	routes.GET("/projects/:projectId/quality-runs", handler.listQualityRuns)
	routes.GET("/quality-runs/:qualityRunId", handler.getQualityRun)
	// Export is an authenticated, side-effect-free representation read. Do not
	// pass it through durable idempotency response capture: an exact ZIP may be
	// up to MaxArchiveBytes and intentionally exceeds the mutation replay limit.
	routes.POST("/projects/:projectId/exports", handler.exportArchive)
	routes.POST("/projects/:projectId/deployments", deliveryHandlers(mutationMiddleware, handler.publishDeployment)...)
	routes.GET("/projects/:projectId/deployments", handler.listDeployments)
	routes.GET("/deployments/:deploymentId", handler.getDeployment)
	routes.GET("/deployments/:deploymentId/logs", handler.deploymentLogs)
	routes.POST("/deployments/:deploymentId/rollback", deliveryHandlers(mutationMiddleware, handler.rollbackDeployment)...)
	return nil
}

// RegisterDeliveryPublicRoutes must be registered on a public router, not the
// authenticated group. LocalStaticAssetService verifies durable ready state
// before delegating to immutable filesystem storage.
func RegisterDeliveryPublicRoutes(routes gin.IRoutes, handler *DeliveryHandler) error {
	if routes == nil || handler == nil {
		return errors.New("delivery public routes and handler are required")
	}
	if handler.staticAssets == nil {
		return errors.New("delivery static asset service is not configured")
	}
	routes.GET("/published/:deploymentId/:versionId/*asset", handler.servePublishedAsset)
	routes.HEAD("/published/:deploymentId/:versionId/*asset", handler.servePublishedAsset)
	return nil
}

func deliveryHandlers(middleware []gin.HandlerFunc, handler gin.HandlerFunc) []gin.HandlerFunc {
	handlers := make([]gin.HandlerFunc, 0, len(middleware)+1)
	handlers = append(handlers, middleware...)
	return append(handlers, handler)
}

type createQualityRunRequest struct {
	WorkspaceRevision core.VersionRef `json:"workspaceRevision"`
}

type exportArchiveRequest struct {
	Kind            delivery.ExportKind `json:"kind"`
	Revision        *core.VersionRef    `json:"revision,omitempty"`
	BuildManifestID string              `json:"buildManifestId,omitempty"`
	RedactSensitive *bool               `json:"redactSensitive,omitempty"`
}

func (h *DeliveryHandler) createQualityRun(c *gin.Context) {
	var request createQualityRunRequest
	if !h.decode(c, &request) {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	report, err := h.quality.Evaluate(c.Request.Context(), c.Param("projectId"), actor, delivery.QualityRunInput{WorkspaceRevision: request.WorkspaceRevision})
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	c.Header("ETag", report.ETag)
	c.Header("Location", "/v1/quality-runs/"+report.ID)
	writeDeliveryJSON(c, http.StatusCreated, gin.H{"qualityRun": report})
}

func (h *DeliveryHandler) getQualityRun(c *gin.Context) {
	if !allowDeliveryQuery(c) {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	report, err := h.quality.Get(c.Request.Context(), c.Param("qualityRunId"), actor)
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	if deliveryNotModified(c, report.ETag) {
		return
	}
	c.Header("ETag", report.ETag)
	writeDeliveryJSON(c, http.StatusOK, gin.H{"qualityRun": report})
}

func (h *DeliveryHandler) listQualityRuns(c *gin.Context) {
	if !allowDeliveryQuery(c, "workspaceRevisionId") {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	reports, err := h.quality.List(c.Request.Context(), c.Param("projectId"), actor, strings.TrimSpace(c.Query("workspaceRevisionId")))
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	if reports == nil {
		reports = []delivery.QualityReport{}
	}
	writeDeliveryJSON(c, http.StatusOK, gin.H{"qualityRuns": reports})
}

func (h *DeliveryHandler) exportArchive(c *gin.Context) {
	var request exportArchiveRequest
	if !h.decode(c, &request) {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	redact := true
	if request.RedactSensitive != nil {
		redact = *request.RedactSensitive
	}
	archive, err := h.export.Export(c.Request.Context(), c.Param("projectId"), actor, delivery.ExportInput{
		Kind: request.Kind, Revision: request.Revision,
		BuildManifestID: strings.TrimSpace(request.BuildManifestID), RedactSensitive: redact,
	})
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	digest := sha256.Sum256(archive.Data)
	c.Header("Cache-Control", "no-store")
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": archive.Filename})
	if disposition == "" {
		disposition = fmt.Sprintf(`attachment; filename="worksflow-%s.zip"`, request.Kind)
	}
	c.Header("Content-Disposition", disposition)
	c.Header("Content-Type", archive.ContentType)
	c.Header("Digest", "sha-256="+base64.StdEncoding.EncodeToString(digest[:]))
	c.Header("ETag", `"`+archive.Checksum+`"`)
	c.Header("X-Archive-File-Count", strconv.Itoa(archive.FileCount))
	c.Header("X-Archive-Redaction-Count", strconv.Itoa(len(archive.Redactions)))
	c.Data(http.StatusOK, archive.ContentType, archive.Data)
}

func (h *DeliveryHandler) publishDeployment(c *gin.Context) {
	var input delivery.PublishInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	expected, ok := optionalDeliveryIfMatch(c, false)
	if !ok {
		return
	}
	deployed, err := h.publish.Publish(c.Request.Context(), c.Param("projectId"), actor, expected, input)
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	c.Header("ETag", deployed.ETag)
	c.Header("Location", "/v1/deployments/"+deployed.ID)
	writeDeliveryJSON(c, http.StatusCreated, gin.H{"deployment": deployed, "absoluteUrl": deployed.PublicURL})
}

func (h *DeliveryHandler) listDeployments(c *gin.Context) {
	if !allowDeliveryQuery(c) {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	deployments, err := h.publish.List(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	if deployments == nil {
		deployments = []delivery.Deployment{}
	}
	writeDeliveryJSON(c, http.StatusOK, gin.H{"deployments": deployments})
}

func (h *DeliveryHandler) getDeployment(c *gin.Context) {
	if !allowDeliveryQuery(c) {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	deployed, err := h.publish.Get(c.Request.Context(), c.Param("deploymentId"), actor)
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	if deliveryNotModified(c, deployed.ETag) {
		return
	}
	c.Header("ETag", deployed.ETag)
	writeDeliveryJSON(c, http.StatusOK, gin.H{"deployment": deployed})
}

func (h *DeliveryHandler) deploymentLogs(c *gin.Context) {
	if !allowDeliveryQuery(c) {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	logs, err := h.publish.Logs(c.Request.Context(), c.Param("deploymentId"), actor)
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	if logs == nil {
		logs = []delivery.DeploymentLog{}
	}
	writeDeliveryJSON(c, http.StatusOK, gin.H{"logs": logs})
}

func (h *DeliveryHandler) rollbackDeployment(c *gin.Context) {
	var input delivery.RollbackInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	expected, ok := optionalDeliveryIfMatch(c, true)
	if !ok {
		return
	}
	deployed, err := h.publish.Rollback(c.Request.Context(), c.Param("deploymentId"), actor, expected, input)
	if err != nil {
		writeDeliveryError(c, err)
		return
	}
	c.Header("ETag", deployed.ETag)
	writeDeliveryJSON(c, http.StatusOK, gin.H{"deployment": deployed, "absoluteUrl": deployed.PublicURL})
}

func (h *DeliveryHandler) servePublishedAsset(c *gin.Context) {
	h.staticAssets.ServeAsset(c.Writer, c.Request, c.Param("deploymentId"), c.Param("versionId"), strings.TrimPrefix(c.Param("asset"), "/"))
}

func (h *DeliveryHandler) decode(c *gin.Context, destination any) bool {
	if err := DecodeJSON(c, destination, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return false
	}
	return true
}

func writeDeliveryJSON(c *gin.Context, status int, value any) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, value)
}

func writeDeliveryError(c *gin.Context, err error) {
	if typed, ok := delivery.AsError(err); ok {
		status := typed.Status
		if status < 400 || status > 599 {
			status = http.StatusInternalServerError
		}
		detail := typed.Detail
		if status >= 500 && typed.Code == delivery.CodeInternal {
			detail = "An unexpected error occurred while processing the delivery operation."
		}
		details := problem.New(status, string(typed.Code), http.StatusText(status), detail)
		details.Errors = typed.Fields
		problem.Write(c, details)
		return
	}
	problem.WriteError(c, err)
}

func optionalDeliveryIfMatch(c *gin.Context, required bool) (string, bool) {
	value := strings.TrimSpace(c.GetHeader("If-Match"))
	if value == "" {
		if required {
			problem.Write(c, problem.New(http.StatusPreconditionRequired, "if_match_required", "Precondition required", "This operation requires the current deployment ETag in If-Match."))
			return "", false
		}
		return "", true
	}
	if value == "*" || strings.HasPrefix(value, "W/") || strings.Contains(value, ",") || !strings.HasPrefix(value, `"`) || !strings.HasSuffix(value, `"`) || strings.ContainsAny(value, "\r\n") {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_if_match", "Invalid If-Match header", "If-Match must contain one strong entity tag."))
		return "", false
	}
	return value, true
}

func deliveryNotModified(c *gin.Context, etag string) bool {
	if etag != "" && strings.TrimSpace(c.GetHeader("If-None-Match")) == etag {
		c.Header("ETag", etag)
		c.Status(http.StatusNotModified)
		return true
	}
	return false
}

func allowDeliveryQuery(c *gin.Context, allowed ...string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range c.Request.URL.Query() {
		if _, ok := set[key]; !ok {
			problem.Write(c, problem.New(http.StatusBadRequest, "invalid_query", "Invalid query parameter", "Unknown query parameter: "+key+"."))
			return false
		}
	}
	return true
}
