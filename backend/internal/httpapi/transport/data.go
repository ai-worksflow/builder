package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/dataruntime"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type DataRuntimeAPI interface {
	Snapshot(context.Context, string, string) (dataruntime.ProjectSnapshot, error)
	ListTables(context.Context, string, string) ([]dataruntime.Table, error)
	GetTable(context.Context, string, string, string) (dataruntime.Table, error)
	CreateTable(context.Context, string, string, dataruntime.TableInput) (dataruntime.Table, error)
	RenameTable(context.Context, string, string, string, string) (dataruntime.Table, error)
	DeleteTable(context.Context, string, string, string) error
	ListRecords(context.Context, string, string, string, int, int) (dataruntime.RecordPage, error)
	GetRecord(context.Context, string, string, string, string) (dataruntime.Record, error)
	CreateRecord(context.Context, string, string, string, dataruntime.RecordInput) (dataruntime.Record, error)
	UpdateRecord(context.Context, string, string, string, string, dataruntime.RecordInput) (dataruntime.Record, error)
	DeleteRecord(context.Context, string, string, string, string) error
	ListMetadata(context.Context, string, string, dataruntime.MetadataKind) ([]dataruntime.MetadataItem, error)
	GetMetadata(context.Context, string, string, string, dataruntime.MetadataKind) (dataruntime.MetadataItem, error)
	CreateMetadata(context.Context, string, string, dataruntime.MetadataKind, map[string]json.RawMessage) (dataruntime.MetadataItem, error)
	UpdateMetadata(context.Context, string, string, string, dataruntime.MetadataKind, map[string]json.RawMessage) (dataruntime.MetadataItem, error)
	DeleteMetadata(context.Context, string, string, string, dataruntime.MetadataKind) error
	ListVariables(context.Context, string, string) ([]dataruntime.EnvironmentVariable, error)
	SetVariable(context.Context, string, string, dataruntime.EnvironmentVariableInput) (dataruntime.EnvironmentVariable, error)
	DeleteVariable(context.Context, string, string, string) error
	PreviewMigration(context.Context, string, string, []dataruntime.MigrationOperation) (dataruntime.MigrationPreview, error)
	ApplyMigration(context.Context, string, string, string) (dataruntime.ApplyMigrationResult, error)
	ConnectSupabase(context.Context, string, string, dataruntime.SupabaseConnectionInput) (dataruntime.SupabaseConnectionResult, error)
}

type DataDependencies struct {
	Service          DataRuntimeAPI
	MaxJSONBodyBytes int64
}

type DataHandler struct {
	service DataRuntimeAPI
	maxBody int64
}

func NewDataHandler(dependencies DataDependencies) (*DataHandler, error) {
	if dependencies.Service == nil {
		return nil, errors.New("data runtime service is required")
	}
	if dependencies.MaxJSONBodyBytes <= 0 {
		dependencies.MaxJSONBodyBytes = dataruntime.MaxRequestBytes
	}
	if dependencies.MaxJSONBodyBytes > dataruntime.MaxRequestBytes {
		dependencies.MaxJSONBodyBytes = dataruntime.MaxRequestBytes
	}
	return &DataHandler{service: dependencies.Service, maxBody: dependencies.MaxJSONBodyBytes}, nil
}

// RegisterDataRoutes is deliberately independent from NewRouter. Pass an
// authenticated group and the existing CSRF/capture-idempotency/persist-
// idempotency handlers as mutationMiddleware. A group rooted at /v1 produces
// /v1/data/... routes.
func RegisterDataRoutes(routes gin.IRoutes, handler *DataHandler, mutationMiddleware ...gin.HandlerFunc) error {
	if routes == nil || handler == nil {
		return errors.New("data runtime routes and handler are required")
	}
	routes.POST("/data/connect/supabase", dataHandlers(mutationMiddleware, handler.connectSupabase)...)
	routes.GET("/data/projects/:projectId", handler.snapshot)
	routes.GET("/data/projects/:projectId/audit", handler.audit)
	routes.GET("/data/projects/:projectId/tables", handler.listTables)
	routes.POST("/data/projects/:projectId/tables", dataHandlers(mutationMiddleware, handler.createTable)...)
	routes.GET("/data/projects/:projectId/tables/:tableId", handler.getTable)
	routes.PATCH("/data/projects/:projectId/tables/:tableId", dataHandlers(mutationMiddleware, handler.renameTable)...)
	routes.DELETE("/data/projects/:projectId/tables/:tableId", dataHandlers(mutationMiddleware, handler.deleteTable)...)
	routes.GET("/data/projects/:projectId/tables/:tableId/records", handler.listRecords)
	routes.POST("/data/projects/:projectId/tables/:tableId/records", dataHandlers(mutationMiddleware, handler.createRecord)...)
	routes.GET("/data/projects/:projectId/tables/:tableId/records/:recordId", handler.getRecord)
	routes.PATCH("/data/projects/:projectId/tables/:tableId/records/:recordId", dataHandlers(mutationMiddleware, handler.updateRecord)...)
	routes.DELETE("/data/projects/:projectId/tables/:tableId/records/:recordId", dataHandlers(mutationMiddleware, handler.deleteRecord)...)
	routes.GET("/data/projects/:projectId/metadata/:kind", handler.listMetadata)
	routes.POST("/data/projects/:projectId/metadata/:kind", dataHandlers(mutationMiddleware, handler.createMetadata)...)
	routes.GET("/data/projects/:projectId/metadata/:kind/:itemId", handler.getMetadata)
	routes.PATCH("/data/projects/:projectId/metadata/:kind/:itemId", dataHandlers(mutationMiddleware, handler.updateMetadata)...)
	routes.DELETE("/data/projects/:projectId/metadata/:kind/:itemId", dataHandlers(mutationMiddleware, handler.deleteMetadata)...)
	routes.GET("/data/projects/:projectId/variables", handler.listVariables)
	routes.POST("/data/projects/:projectId/variables", dataHandlers(mutationMiddleware, handler.setVariable)...)
	routes.DELETE("/data/projects/:projectId/variables/:variableId", dataHandlers(mutationMiddleware, handler.deleteVariable)...)
	routes.POST("/data/projects/:projectId/migrations/preview", dataHandlers(mutationMiddleware, handler.previewMigration)...)
	routes.POST("/data/projects/:projectId/migrations/apply", dataHandlers(mutationMiddleware, handler.applyMigration)...)
	return nil
}

func dataHandlers(middleware []gin.HandlerFunc, handler gin.HandlerFunc) []gin.HandlerFunc {
	handlers := make([]gin.HandlerFunc, 0, len(middleware)+1)
	handlers = append(handlers, middleware...)
	handlers = append(handlers, handler)
	return handlers
}

type renameDataTableInput struct {
	Name string `json:"name"`
}

func (h *DataHandler) snapshot(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	value, err := h.service.Snapshot(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"project": value})
}

func (h *DataHandler) audit(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	value, err := h.service.Snapshot(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"audit": value.Audit})
}

func (h *DataHandler) listTables(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	items, err := h.service.ListTables(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"tables": items})
}

func (h *DataHandler) createTable(c *gin.Context) {
	var input dataruntime.TableInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	table, err := h.service.CreateTable(c.Request.Context(), c.Param("projectId"), actor, input)
	if err != nil {
		writeDataError(c, err)
		return
	}
	c.Header("Location", c.Request.URL.Path+"/"+table.ID)
	writeDataJSON(c, http.StatusCreated, gin.H{"table": table})
}

func (h *DataHandler) getTable(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	table, err := h.service.GetTable(c.Request.Context(), c.Param("projectId"), c.Param("tableId"), actor)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"table": table})
}

func (h *DataHandler) renameTable(c *gin.Context) {
	var input renameDataTableInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	table, err := h.service.RenameTable(c.Request.Context(), c.Param("projectId"), c.Param("tableId"), actor, input.Name)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"table": table})
}

func (h *DataHandler) deleteTable(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	id := c.Param("tableId")
	if err := h.service.DeleteTable(c.Request.Context(), c.Param("projectId"), id, actor); err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"deleted": true, "id": id})
}

func (h *DataHandler) listRecords(c *gin.Context) {
	limit, offset, ok := dataPagination(c)
	if !ok {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	page, err := h.service.ListRecords(c.Request.Context(), c.Param("projectId"), c.Param("tableId"), actor, limit, offset)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, page)
}

func (h *DataHandler) createRecord(c *gin.Context) {
	var input dataruntime.RecordInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	record, err := h.service.CreateRecord(c.Request.Context(), c.Param("projectId"), c.Param("tableId"), actor, input)
	if err != nil {
		writeDataError(c, err)
		return
	}
	c.Header("Location", c.Request.URL.Path+"/"+record.ID)
	writeDataJSON(c, http.StatusCreated, gin.H{"record": record})
}

func (h *DataHandler) getRecord(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	record, err := h.service.GetRecord(c.Request.Context(), c.Param("projectId"), c.Param("tableId"), c.Param("recordId"), actor)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"record": record})
}

func (h *DataHandler) updateRecord(c *gin.Context) {
	var input dataruntime.RecordInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	record, err := h.service.UpdateRecord(c.Request.Context(), c.Param("projectId"), c.Param("tableId"), c.Param("recordId"), actor, input)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"record": record})
}

func (h *DataHandler) deleteRecord(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	id := c.Param("recordId")
	if err := h.service.DeleteRecord(c.Request.Context(), c.Param("projectId"), c.Param("tableId"), id, actor); err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"deleted": true, "id": id})
}

func metadataKind(c *gin.Context) (dataruntime.MetadataKind, bool) {
	kind, err := dataruntime.ParseMetadataKind(c.Param("kind"))
	if err != nil {
		writeDataError(c, err)
		return "", false
	}
	return kind, true
}

func (h *DataHandler) listMetadata(c *gin.Context) {
	kind, ok := metadataKind(c)
	if !ok {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	items, err := h.service.ListMetadata(c.Request.Context(), c.Param("projectId"), actor, kind)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"kind": kind, "items": items})
}

func (h *DataHandler) getMetadata(c *gin.Context) {
	kind, ok := metadataKind(c)
	if !ok {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	item, err := h.service.GetMetadata(c.Request.Context(), c.Param("projectId"), c.Param("itemId"), actor, kind)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"item": item})
}

func (h *DataHandler) createMetadata(c *gin.Context) {
	h.mutateMetadata(c, true)
}

func (h *DataHandler) updateMetadata(c *gin.Context) {
	h.mutateMetadata(c, false)
}

func (h *DataHandler) mutateMetadata(c *gin.Context, create bool) {
	kind, ok := metadataKind(c)
	if !ok {
		return
	}
	input := map[string]json.RawMessage{}
	if !h.decode(c, &input) {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	var item dataruntime.MetadataItem
	var err error
	if create {
		item, err = h.service.CreateMetadata(c.Request.Context(), c.Param("projectId"), actor, kind, input)
	} else {
		item, err = h.service.UpdateMetadata(c.Request.Context(), c.Param("projectId"), c.Param("itemId"), actor, kind, input)
	}
	if err != nil {
		writeDataError(c, err)
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
		c.Header("Location", c.Request.URL.Path+"/"+item.ID)
	}
	writeDataJSON(c, status, gin.H{"item": item})
}

func (h *DataHandler) deleteMetadata(c *gin.Context) {
	kind, ok := metadataKind(c)
	if !ok {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	id := c.Param("itemId")
	if err := h.service.DeleteMetadata(c.Request.Context(), c.Param("projectId"), id, actor, kind); err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"deleted": true, "id": id})
}

func (h *DataHandler) listVariables(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	items, err := h.service.ListVariables(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"variables": items})
}

func (h *DataHandler) setVariable(c *gin.Context) {
	var input dataruntime.EnvironmentVariableInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	item, err := h.service.SetVariable(c.Request.Context(), c.Param("projectId"), actor, input)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusCreated, gin.H{"variable": item})
}

func (h *DataHandler) deleteVariable(c *gin.Context) {
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	id := c.Param("variableId")
	if err := h.service.DeleteVariable(c.Request.Context(), c.Param("projectId"), id, actor); err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"deleted": true, "id": id})
}

func (h *DataHandler) previewMigration(c *gin.Context) {
	var input dataruntime.MigrationPreviewInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	preview, err := h.service.PreviewMigration(c.Request.Context(), c.Param("projectId"), actor, input.Operations)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"preview": preview})
}

func (h *DataHandler) applyMigration(c *gin.Context) {
	var input dataruntime.ApplyMigrationInput
	if !h.decode(c, &input) {
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	result, err := h.service.ApplyMigration(c.Request.Context(), c.Param("projectId"), actor, input.ConfirmationToken)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, result)
}

func (h *DataHandler) connectSupabase(c *gin.Context) {
	var input dataruntime.SupabaseConnectionInput
	if !h.decode(c, &input) {
		return
	}
	projectID := strings.TrimSpace(c.GetHeader("X-Worksflow-Project-Id"))
	if projectID == "" {
		writeDataError(c, dataruntime.Invalid("projectId", "X-Worksflow-Project-Id is required"))
		return
	}
	actor, ok := dataActor(c)
	if !ok {
		return
	}
	result, err := h.service.ConnectSupabase(c.Request.Context(), projectID, actor, input)
	if err != nil {
		writeDataError(c, err)
		return
	}
	writeDataJSON(c, http.StatusOK, gin.H{"connection": result})
}

func (h *DataHandler) decode(c *gin.Context, destination any) bool {
	if err := DecodeJSON(c, destination, h.maxBody); err != nil {
		writeDataError(c, dataDecodeError(err))
		return false
	}
	return true
}

func dataDecodeError(err error) error {
	var maxBytesError *http.MaxBytesError
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	switch {
	case errors.Is(err, errUnsupportedJSONType):
		return dataruntime.NewError(dataruntime.CodeInvalidRequest, http.StatusUnsupportedMediaType, err.Error())
	case errors.As(err, &maxBytesError):
		return dataruntime.NewError(dataruntime.CodeRequestTooLarge, http.StatusRequestEntityTooLarge, "The JSON request body exceeds the configured limit")
	case errors.Is(err, errEmptyJSONBody), errors.Is(err, errMultipleJSONValues):
		return dataruntime.NewError(dataruntime.CodeInvalidRequest, http.StatusBadRequest, err.Error())
	case errors.As(err, &syntaxError):
		return dataruntime.NewError(dataruntime.CodeInvalidRequest, http.StatusBadRequest, fmt.Sprintf("Malformed JSON near byte %d", syntaxError.Offset))
	case errors.As(err, &typeError):
		return dataruntime.Invalid(typeError.Field, "Field "+typeError.Field+" has an invalid value type")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return dataruntime.NewError(dataruntime.ErrorCode("unknown_json_field"), http.StatusBadRequest, err.Error())
	default:
		return dataruntime.NewError(dataruntime.CodeInvalidRequest, http.StatusBadRequest, "The request body is not valid JSON")
	}
}

func dataPagination(c *gin.Context) (int, int, bool) {
	for key := range c.Request.URL.Query() {
		if key != "limit" && key != "offset" {
			writeDataError(c, dataruntime.Invalid(key, "unknown query parameter "+key))
			return 0, 0, false
		}
	}
	limit, offset := 50, 0
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeDataError(c, dataruntime.Invalid("limit", "limit must be between 1 and 100"))
			return 0, 0, false
		}
		limit = parsed
	}
	if value := c.Query("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > dataruntime.MaxRecordsPerTable {
			writeDataError(c, dataruntime.Invalid("offset", "offset must be a non-negative integer within the table limit"))
			return 0, 0, false
		}
		offset = parsed
	}
	return limit, offset, true
}

func dataActor(c *gin.Context) (string, bool) {
	identity, ok := worksmiddleware.GetIdentity(c)
	if !ok || strings.TrimSpace(identity.Session.User.ID) == "" {
		problem.Write(c, problem.New(http.StatusUnauthorized, "authentication_required", "Authentication required", "A valid session is required."))
		return "", false
	}
	return identity.Session.User.ID, true
}

func writeDataJSON(c *gin.Context, status int, value any) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.JSON(status, value)
}

func writeDataError(c *gin.Context, err error) {
	c.Header("Cache-Control", "no-store")
	status := http.StatusInternalServerError
	code := "internal_error"
	title := "Internal server error"
	detail := "The data operation could not be completed."
	fieldErrors := map[string][]string(nil)
	if dataError, ok := dataruntime.AsRuntimeError(err); ok {
		status = dataError.Status
		if status < 400 {
			status = http.StatusInternalServerError
		}
		code = string(dataError.Code)
		title = http.StatusText(status)
		detail = dataError.Message
		fieldErrors = dataError.Fields
	} else {
		switch {
		case errors.Is(err, core.ErrNotFound):
			status, code, title, detail = http.StatusNotFound, "not_found", "Resource not found", "The requested resource was not found."
		case errors.Is(err, core.ErrForbidden):
			status, code, title, detail = http.StatusForbidden, "forbidden", "Operation forbidden", "The current user is not permitted to perform this operation."
		case errors.Is(err, core.ErrInvalidInput):
			status, code, title, detail = http.StatusUnprocessableEntity, "invalid_input", "Input is invalid", "One or more input values are invalid."
		case errors.Is(err, core.ErrConflict):
			status, code, title, detail = http.StatusConflict, "conflict", "Resource conflict", "The resource changed or conflicts with the requested operation."
		}
	}
	details := flattenDataErrors(fieldErrors)
	response := dataProblemResponse{
		Type: "urn:worksflow:problem:" + code, Title: title, Status: status,
		Detail: detail, Instance: c.Request.URL.Path, Code: code,
		RequestID: c.GetString("request_id"), Errors: fieldErrors,
		Error: dataCompatibilityError{Code: code, Message: detail, Details: details},
	}
	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(status, response)
}

// dataProblemResponse is RFC 9457. error is an extension member retained while
// the current frontend DataClient migrates from its legacy error envelope.
type dataProblemResponse struct {
	Type      string                 `json:"type"`
	Title     string                 `json:"title"`
	Status    int                    `json:"status"`
	Detail    string                 `json:"detail,omitempty"`
	Instance  string                 `json:"instance,omitempty"`
	Code      string                 `json:"code,omitempty"`
	RequestID string                 `json:"requestId,omitempty"`
	Errors    map[string][]string    `json:"errors,omitempty"`
	Error     dataCompatibilityError `json:"error"`
}

type dataCompatibilityError struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
}

func flattenDataErrors(fields map[string][]string) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []string{}
	for _, key := range keys {
		result = append(result, fields[key]...)
	}
	return result
}
