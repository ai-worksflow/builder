package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/dataruntime"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

type PublicDataRuntimeAPI interface {
	ListPolicies(context.Context, string, string) ([]dataruntime.PublicTablePolicy, error)
	PutPolicy(context.Context, string, string, string, uint64, dataruntime.PublicTablePolicyInput) (dataruntime.PublicTablePolicy, error)
	DeletePolicy(context.Context, string, string, string, uint64) error
	ActiveDeploymentRuntimeForActor(context.Context, string, string, string) (dataruntime.PublicDeploymentRuntime, error)
	RevokeDeploymentForActor(context.Context, string, string, string) error

	Authenticate(context.Context, string, string) (dataruntime.PublicCapability, error)
	ValidateOrigin(dataruntime.PublicCapability, string) error
	PreflightOrigins(context.Context, string) ([]string, error)
	ListPublicTables(context.Context, dataruntime.PublicCapability) ([]dataruntime.PublicTable, error)
	GetPublicTable(context.Context, dataruntime.PublicCapability, string) (dataruntime.PublicTable, error)
	ListPublicRecords(context.Context, dataruntime.PublicCapability, string, int, int) (dataruntime.RecordPage, error)
	GetPublicRecord(context.Context, dataruntime.PublicCapability, string, string) (dataruntime.Record, error)
	CreatePublicRecord(context.Context, dataruntime.PublicCapability, string, string, dataruntime.RecordInput) (dataruntime.Record, error)
	UpdatePublicRecord(context.Context, dataruntime.PublicCapability, string, string, string, dataruntime.RecordInput) (dataruntime.Record, error)
	DeletePublicRecord(context.Context, dataruntime.PublicCapability, string, string, string) error
}

type PublicDataDependencies struct {
	Service          PublicDataRuntimeAPI
	RateLimiter      dataruntime.PublicRateLimiter
	MaxJSONBodyBytes int64
	ClientKey        func(*gin.Context) string
}

type PublicDataHandler struct {
	service   PublicDataRuntimeAPI
	limiter   dataruntime.PublicRateLimiter
	maxBody   int64
	clientKey func(*gin.Context) string
}

func NewPublicDataHandler(dependencies PublicDataDependencies) (*PublicDataHandler, error) {
	if dependencies.Service == nil || dependencies.RateLimiter == nil {
		return nil, errors.New("public data runtime service and fail-closed rate limiter are required")
	}
	if dependencies.MaxJSONBodyBytes <= 0 {
		dependencies.MaxJSONBodyBytes = dataruntime.MaxPublicRequestBytes
	}
	if dependencies.MaxJSONBodyBytes > dataruntime.MaxPublicRequestBytes {
		dependencies.MaxJSONBodyBytes = dataruntime.MaxPublicRequestBytes
	}
	if dependencies.ClientKey == nil {
		dependencies.ClientKey = publicDataRemoteAddress
	}
	return &PublicDataHandler{
		service: dependencies.Service, limiter: dependencies.RateLimiter,
		maxBody: dependencies.MaxJSONBodyBytes, clientKey: dependencies.ClientKey,
	}, nil
}

// RegisterPublicDataRoutes must be called on a group outside builder session,
// CSRF, and cookie middleware. A group rooted at /v1 exposes only Bearer-token
// routes under /v1/public/data/deployments/:deploymentId.
func RegisterPublicDataRoutes(
	routes gin.IRoutes,
	handler *PublicDataHandler,
	idempotencyStores ...worksmiddleware.IdempotencyStore,
) error {
	if routes == nil || handler == nil {
		return errors.New("public data routes and handler are required")
	}
	var idempotency worksmiddleware.IdempotencyStore
	if len(idempotencyStores) > 0 {
		idempotency = idempotencyStores[0]
	}
	base := "/public/data/deployments/:deploymentId"
	routes.OPTIONS(base+"/*path", handler.preflight)
	routes.GET(base+"/tables", handler.listTables)
	routes.GET(base+"/tables/:tableId", handler.getTable)
	routes.GET(base+"/tables/:tableId/records", handler.listRecords)
	routes.POST(base+"/tables/:tableId/records", publicDataMutationHandlers(handler, dataruntime.PublicOperationCreate, idempotency, handler.createRecord)...)
	routes.GET(base+"/tables/:tableId/records/:recordId", handler.getRecord)
	routes.PATCH(base+"/tables/:tableId/records/:recordId", publicDataMutationHandlers(handler, dataruntime.PublicOperationUpdate, idempotency, handler.updateRecord)...)
	routes.DELETE(base+"/tables/:tableId/records/:recordId", publicDataMutationHandlers(handler, dataruntime.PublicOperationDelete, idempotency, handler.deleteRecord)...)
	return nil
}

func publicDataMutationHandlers(
	handler *PublicDataHandler,
	operation dataruntime.PublicDataOperation,
	idempotency worksmiddleware.IdempotencyStore,
	final gin.HandlerFunc,
) []gin.HandlerFunc {
	return []gin.HandlerFunc{
		handler.requireCapability(operation),
		worksmiddleware.CaptureIdempotencyKey(true),
		worksmiddleware.PersistPublicIdempotency(idempotency),
		final,
	}
}

// RegisterPublicDataManagementRoutes belongs on the authenticated builder /v1
// group. Pass the same CSRF/idempotency middleware used by other mutations.
func RegisterPublicDataManagementRoutes(routes gin.IRoutes, handler *PublicDataHandler, mutationMiddleware ...gin.HandlerFunc) error {
	if routes == nil || handler == nil {
		return errors.New("public data management routes and handler are required")
	}
	base := "/data/projects/:projectId/public-runtime"
	routes.GET(base+"/policies", handler.listPolicies)
	routes.PUT(base+"/policies/:tableId", dataHandlers(mutationMiddleware, handler.putPolicy)...)
	routes.DELETE(base+"/policies/:tableId", dataHandlers(mutationMiddleware, handler.deletePolicy)...)
	routes.GET(base+"/deployments/:deploymentId", handler.activeDeploymentRuntime)
	routes.DELETE(base+"/deployments/:deploymentId", dataHandlers(mutationMiddleware, handler.revokeDeployment)...)
	return nil
}

func (h *PublicDataHandler) listPolicies(c *gin.Context) {
	actorID, ok := dataActor(c)
	if !ok {
		return
	}
	policies, err := h.service.ListPolicies(c.Request.Context(), c.Param("projectId"), actorID)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"policies": policies})
}

func (h *PublicDataHandler) putPolicy(c *gin.Context) {
	expectedVersion, ok := publicPolicyExpectedVersion(c)
	if !ok {
		return
	}
	var input dataruntime.PublicTablePolicyInput
	if !h.decode(c, &input) {
		return
	}
	actorID, ok := dataActor(c)
	if !ok {
		return
	}
	policy, err := h.service.PutPolicy(c.Request.Context(), c.Param("projectId"), c.Param("tableId"), actorID, expectedVersion, input)
	if err != nil {
		writeDataError(c, err)
		return
	}
	c.Header("ETag", policy.ETag)
	writeDataJSON(c, http.StatusOK, gin.H{"policy": policy})
}

func (h *PublicDataHandler) deletePolicy(c *gin.Context) {
	expectedVersion, ok := publicPolicyExpectedVersion(c)
	if !ok {
		return
	}
	actorID, ok := dataActor(c)
	if !ok {
		return
	}
	tableID := c.Param("tableId")
	if err := h.service.DeletePolicy(c.Request.Context(), c.Param("projectId"), tableID, actorID, expectedVersion); err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"deleted": true, "tableId": tableID})
}

func publicPolicyExpectedVersion(c *gin.Context) (uint64, bool) {
	value := strings.TrimSpace(c.GetHeader("If-Match"))
	if value == "" {
		writeDataError(c, dataruntime.NewError(dataruntime.ErrorCode("if_match_required"), http.StatusPreconditionRequired, "The current public table policy ETag is required in If-Match"))
		return 0, false
	}
	if strings.HasPrefix(value, "W/") || strings.Contains(value, ",") || value == "*" {
		writeDataError(c, dataruntime.NewError(dataruntime.ErrorCode("invalid_if_match"), http.StatusBadRequest, "If-Match must contain one strong public table policy ETag"))
		return 0, false
	}
	unquoted, err := strconv.Unquote(value)
	if err != nil {
		writeDataError(c, dataruntime.NewError(dataruntime.ErrorCode("invalid_if_match"), http.StatusBadRequest, "If-Match must contain one strong public table policy ETag"))
		return 0, false
	}
	prefix := "public-data-policy:" + c.Param("projectId") + ":" + c.Param("tableId") + ":"
	if !strings.HasPrefix(unquoted, prefix) {
		writeDataError(c, dataruntime.NewError(dataruntime.ErrorCode("invalid_if_match"), http.StatusBadRequest, "If-Match does not identify this public table policy"))
		return 0, false
	}
	version, err := strconv.ParseUint(strings.TrimPrefix(unquoted, prefix), 10, 64)
	if err != nil {
		writeDataError(c, dataruntime.NewError(dataruntime.ErrorCode("invalid_if_match"), http.StatusBadRequest, "If-Match contains an invalid public table policy version"))
		return 0, false
	}
	return version, true
}

func (h *PublicDataHandler) activeDeploymentRuntime(c *gin.Context) {
	actorID, ok := dataActor(c)
	if !ok {
		return
	}
	runtime, err := h.service.ActiveDeploymentRuntimeForActor(c.Request.Context(), c.Param("projectId"), c.Param("deploymentId"), actorID)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"runtime": runtime})
}

func (h *PublicDataHandler) revokeDeployment(c *gin.Context) {
	actorID, ok := dataActor(c)
	if !ok {
		return
	}
	deploymentID := c.Param("deploymentId")
	if err := h.service.RevokeDeploymentForActor(c.Request.Context(), c.Param("projectId"), deploymentID, actorID); err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"revoked": true, "deploymentId": deploymentID})
}

func (h *PublicDataHandler) preflight(c *gin.Context) {
	c.Header("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	requestedMethod := strings.ToUpper(strings.TrimSpace(c.GetHeader("Access-Control-Request-Method")))
	if origin == "" || !publicDataMethod(requestedMethod) || !publicDataHeadersAllowed(c.GetHeader("Access-Control-Request-Headers")) {
		writeDataError(c, dataruntime.NewError(dataruntime.CodePublicOriginDenied, http.StatusForbidden, "The public data preflight request is not allowed"))
		return
	}
	origins, err := h.service.PreflightOrigins(c.Request.Context(), c.Param("deploymentId"))
	if err != nil || !publicDataOriginAllowed(origin, origins) {
		writeDataError(c, dataruntime.NewError(dataruntime.CodePublicOriginDenied, http.StatusForbidden, "The public data preflight request is not allowed"))
		return
	}
	setPublicDataCORS(c, origin)
	c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
	c.Header("Access-Control-Max-Age", "600")
	c.Status(http.StatusNoContent)
}

func (h *PublicDataHandler) listTables(c *gin.Context) {
	capability, ok := h.capability(c, dataruntime.PublicOperationRead)
	if !ok {
		return
	}
	tables, err := h.service.ListPublicTables(c.Request.Context(), capability)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writePublicDataJSON(c, http.StatusOK, gin.H{"tables": tables})
}

func (h *PublicDataHandler) getTable(c *gin.Context) {
	capability, ok := h.capability(c, dataruntime.PublicOperationRead)
	if !ok {
		return
	}
	table, err := h.service.GetPublicTable(c.Request.Context(), capability, c.Param("tableId"))
	if err != nil {
		writeDataError(c, err)
		return
	}
	writePublicDataJSON(c, http.StatusOK, gin.H{"table": table})
}

func (h *PublicDataHandler) listRecords(c *gin.Context) {
	limit, offset, ok := dataPagination(c)
	if !ok {
		return
	}
	capability, ok := h.capability(c, dataruntime.PublicOperationRead)
	if !ok {
		return
	}
	page, err := h.service.ListPublicRecords(c.Request.Context(), capability, c.Param("tableId"), limit, offset)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writePublicDataJSON(c, http.StatusOK, page)
}

func (h *PublicDataHandler) getRecord(c *gin.Context) {
	capability, ok := h.capability(c, dataruntime.PublicOperationRead)
	if !ok {
		return
	}
	record, err := h.service.GetPublicRecord(c.Request.Context(), capability, c.Param("tableId"), c.Param("recordId"))
	if err != nil {
		writeDataError(c, err)
		return
	}
	writePublicDataJSON(c, http.StatusOK, gin.H{"record": record})
}

func (h *PublicDataHandler) createRecord(c *gin.Context) {
	capability, ok := h.capability(c, dataruntime.PublicOperationCreate)
	if !ok {
		return
	}
	var input dataruntime.RecordInput
	if !h.decode(c, &input) {
		return
	}
	record, err := h.service.CreatePublicRecord(c.Request.Context(), capability, c.Param("tableId"), c.GetString("request_id"), input)
	if err != nil {
		writeDataError(c, err)
		return
	}
	c.Header("Location", c.Request.URL.Path+"/"+record.ID)
	writePublicDataJSON(c, http.StatusCreated, gin.H{"record": record})
}

func (h *PublicDataHandler) updateRecord(c *gin.Context) {
	capability, ok := h.capability(c, dataruntime.PublicOperationUpdate)
	if !ok {
		return
	}
	var input dataruntime.RecordInput
	if !h.decode(c, &input) {
		return
	}
	record, err := h.service.UpdatePublicRecord(c.Request.Context(), capability, c.Param("tableId"), c.Param("recordId"), c.GetString("request_id"), input)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writePublicDataJSON(c, http.StatusOK, gin.H{"record": record})
}

func (h *PublicDataHandler) deleteRecord(c *gin.Context) {
	capability, ok := h.capability(c, dataruntime.PublicOperationDelete)
	if !ok {
		return
	}
	recordID := c.Param("recordId")
	if err := h.service.DeletePublicRecord(c.Request.Context(), capability, c.Param("tableId"), recordID, c.GetString("request_id")); err != nil {
		writeDataError(c, err)
		return
	}
	writePublicDataJSON(c, http.StatusOK, gin.H{"deleted": true, "recordId": recordID})
}

func (h *PublicDataHandler) capability(c *gin.Context, operation dataruntime.PublicDataOperation) (dataruntime.PublicCapability, bool) {
	if value, exists := c.Get(publicDataCapabilityContextKey); exists {
		if capability, ok := value.(dataruntime.PublicCapability); ok {
			return capability, true
		}
	}
	return h.authenticateCapability(c, operation)
}

const publicDataCapabilityContextKey = "public_data_capability"

func (h *PublicDataHandler) requireCapability(operation dataruntime.PublicDataOperation) gin.HandlerFunc {
	return func(c *gin.Context) {
		capability, ok := h.authenticateCapability(c, operation)
		if !ok {
			return
		}
		if !worksmiddleware.SetPublicIdempotencyIdentity(c, capability.ID, capability.DeploymentID) {
			writeDataError(c, dataruntime.NewError(dataruntime.CodePublicRuntimeUnavailable, http.StatusServiceUnavailable, "The public data runtime is temporarily unavailable"))
			return
		}
		c.Set(publicDataCapabilityContextKey, capability)
		c.Next()
	}
}

func (h *PublicDataHandler) authenticateCapability(c *gin.Context, operation dataruntime.PublicDataOperation) (dataruntime.PublicCapability, bool) {
	token, ok := publicDataBearer(c.GetHeader("Authorization"))
	if !ok {
		writeDataError(c, dataruntime.NewError(dataruntime.CodePublicCapabilityInvalid, http.StatusUnauthorized, "The public data capability is invalid or expired"))
		return dataruntime.PublicCapability{}, false
	}
	capability, err := h.service.Authenticate(c.Request.Context(), c.Param("deploymentId"), token)
	if err != nil {
		writeDataError(c, err)
		return dataruntime.PublicCapability{}, false
	}
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if err := h.service.ValidateOrigin(capability, origin); err != nil {
		writeDataError(c, err)
		return dataruntime.PublicCapability{}, false
	}
	if origin != "" {
		setPublicDataCORS(c, origin)
	}
	decision, err := h.limiter.Allow(c.Request.Context(), dataruntime.PublicRateLimitRequest{
		CapabilityID: capability.ID, ClientKey: h.clientKey(c), Operation: operation,
	})
	if err != nil {
		writeDataError(c, dataruntime.NewError(dataruntime.CodePublicRuntimeUnavailable, http.StatusServiceUnavailable, "The public data runtime is temporarily unavailable"))
		return dataruntime.PublicCapability{}, false
	}
	c.Header("X-RateLimit-Limit", strconv.FormatInt(decision.Limit, 10))
	c.Header("X-RateLimit-Remaining", strconv.FormatInt(decision.Remaining, 10))
	if !decision.Allowed {
		retrySeconds := int64((decision.RetryAfter + time.Second - 1) / time.Second)
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		c.Header("Retry-After", strconv.FormatInt(retrySeconds, 10))
		writeDataError(c, dataruntime.NewError(dataruntime.CodePublicRateLimited, http.StatusTooManyRequests, "The public data request rate limit was exceeded"))
		return dataruntime.PublicCapability{}, false
	}
	return capability, true
}

func (h *PublicDataHandler) decode(c *gin.Context, destination any) bool {
	if err := DecodeJSON(c, destination, h.maxBody); err != nil {
		writeDataError(c, dataDecodeError(err))
		return false
	}
	return true
}

func publicDataBearer(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], "wfpub_") {
		return "", false
	}
	return parts[1], true
}

func publicDataRemoteAddress(c *gin.Context) string {
	value := strings.TrimSpace(c.Request.RemoteAddr)
	if host, _, err := net.SplitHostPort(value); err == nil && host != "" {
		return host
	}
	if value == "" {
		return "unknown"
	}
	return value
}

func publicDataOriginAllowed(origin string, allowed []string) bool {
	normalized, err := dataruntime.NormalizePublicOrigins([]string{origin})
	if err != nil {
		return false
	}
	for _, candidate := range allowed {
		if candidate == normalized[0] {
			return true
		}
	}
	return false
}

func publicDataMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func publicDataHeadersAllowed(header string) bool {
	for _, raw := range strings.Split(header, ",") {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value != "" && value != "authorization" && value != "content-type" && value != "idempotency-key" {
			return false
		}
	}
	return true
}

func setPublicDataCORS(c *gin.Context, origin string) {
	c.Header("Access-Control-Allow-Origin", origin)
	c.Header("Access-Control-Expose-Headers", "Idempotency-Replayed, Location, Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining")
	addVary(c.Writer.Header(), "Origin")
}

func addVary(header http.Header, value string) {
	for _, current := range header.Values("Vary") {
		for _, item := range strings.Split(current, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func writePublicDataJSON(c *gin.Context, status int, value any) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cross-Origin-Resource-Policy", "cross-origin")
	c.JSON(status, value)
}
