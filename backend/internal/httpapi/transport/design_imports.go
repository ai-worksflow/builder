package transport

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/designimport"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type DesignImportAPI interface {
	Capabilities(context.Context, string, string) (designimport.Capabilities, error)
	Create(context.Context, string, string, string, designimport.CreateInput) (designimport.Import, error)
	Get(context.Context, string, string) (designimport.Import, error)
	List(context.Context, string, string, string) ([]designimport.Import, error)
	Decide(context.Context, string, string, string, designimport.DecisionInput) (designimport.Import, error)
}

type DesignImportDependencies struct {
	Service          DesignImportAPI
	MaxJSONBodyBytes int64
}

type DesignImportHandler struct {
	service DesignImportAPI
	maxBody int64
}

func NewDesignImportHandler(dependencies DesignImportDependencies) (*DesignImportHandler, error) {
	if dependencies.Service == nil {
		return nil, errors.New("design import service is required")
	}
	maxBody := dependencies.MaxJSONBodyBytes
	if maxBody <= 0 || maxBody > designimport.MaxRequestBytes {
		maxBody = designimport.MaxRequestBytes
	}
	return &DesignImportHandler{service: dependencies.Service, maxBody: maxBody}, nil
}

func RegisterDesignImportRoutes(group *gin.RouterGroup, handler *DesignImportHandler, mutation ...gin.HandlerFunc) error {
	if group == nil || handler == nil {
		return errors.New("design import route group and handler are required")
	}
	command := func(path string, endpoint gin.HandlerFunc) {
		handlers := append([]gin.HandlerFunc{}, mutation...)
		handlers = append(handlers, endpoint)
		group.POST(path, handlers...)
	}
	conditionalCommand := func(path string, endpoint gin.HandlerFunc) {
		handlers := []gin.HandlerFunc{worksmiddleware.RequireIfMatch()}
		handlers = append(handlers, mutation...)
		handlers = append(handlers, endpoint)
		group.POST(path, handlers...)
	}
	group.GET("/projects/:projectId/design-import-capabilities", handler.GetCapabilities)
	group.GET("/projects/:projectId/design-imports", handler.List)
	group.GET("/design-imports/:designImportId", handler.Get)
	command("/projects/:projectId/design-imports", handler.Create)
	conditionalCommand("/design-imports/:designImportId/decision", handler.Decide)
	return nil
}

func (h *DesignImportHandler) GetCapabilities(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	capabilities, err := h.service.Capabilities(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeDesignImportProblem(c, err)
		return
	}
	c.Header("Cache-Control", "private, max-age=60")
	c.JSON(http.StatusOK, capabilities)
}

func (h *DesignImportHandler) Create(c *gin.Context) {
	var input designimport.CreateInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	created, err := h.service.Create(
		c.Request.Context(), c.Param("projectId"), actor,
		worksmiddleware.IdempotencyKey(c), input,
	)
	if err != nil {
		writeDesignImportProblem(c, err)
		return
	}
	c.Header("ETag", created.ETag)
	c.Header("Location", "/v1/design-imports/"+created.ID)
	c.JSON(http.StatusCreated, created)
}

func (h *DesignImportHandler) Get(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	item, err := h.service.Get(c.Request.Context(), c.Param("designImportId"), actor)
	if err != nil {
		writeDesignImportProblem(c, err)
		return
	}
	c.Header("ETag", item.ETag)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, item)
}

func (h *DesignImportHandler) List(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	items, err := h.service.List(c.Request.Context(), c.Param("projectId"), actor, strings.TrimSpace(c.Query("status")))
	if err != nil {
		writeDesignImportProblem(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writePage(c, items)
}

func (h *DesignImportHandler) Decide(c *gin.Context) {
	var input designimport.DecisionInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	item, err := h.service.Decide(
		c.Request.Context(), c.Param("designImportId"), actor,
		worksmiddleware.IfMatch(c), input,
	)
	if err != nil {
		if errors.Is(err, designimport.ErrConflict) {
			preconditionFailed(c, "design import")
			return
		}
		writeDesignImportProblem(c, err)
		return
	}
	c.Header("ETag", item.ETag)
	c.JSON(http.StatusOK, item)
}

func writeDesignImportProblem(c *gin.Context, err error) {
	var importError *designimport.Error
	switch {
	case errors.Is(err, designimport.ErrInvalidInput):
		details := problem.New(http.StatusUnprocessableEntity, "invalid_design_import", "Design import input is invalid", "One or more design import fields are invalid.")
		if errors.As(err, &importError) && importError.Field != "" {
			details.Errors = map[string][]string{importError.Field: {importError.Detail}}
		}
		problem.Write(c, details)
	case errors.Is(err, designimport.ErrUnsupportedMediaType):
		details := problem.New(http.StatusUnsupportedMediaType, "unsupported_design_import_media", "Design import media is unsupported", "The uploaded file type, extension, or content signature is not accepted.")
		if errors.As(err, &importError) && importError.Field != "" {
			details.Errors = map[string][]string{importError.Field: {importError.Detail}}
		}
		problem.Write(c, details)
	case errors.Is(err, designimport.ErrUploadTooLarge):
		problem.Write(c, problem.New(http.StatusRequestEntityTooLarge, "design_import_too_large", "Design import is too large", "The decoded upload exceeds the server limit."))
	case errors.Is(err, designimport.ErrCapabilityUnavailable):
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "design_import_capability_unavailable", "Design connector is not configured", "This deployment does not fetch remote design URLs. Export the source and upload its file instead."))
	case errors.Is(err, designimport.ErrProcessing):
		c.Header("Retry-After", "1")
		// A retryable processing response must not be sealed as a completed
		// idempotency replay. PersistIdempotency releases 5xx claims, allowing the
		// same command key to recover the durable Design Import lease later.
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "design_import_processing", "Design import is processing", "Another worker holds the durable creation lease. Retry with the same idempotency key after the current checkpoint completes or the lease expires."))
	case errors.Is(err, designimport.ErrConflict):
		problem.Write(c, problem.New(http.StatusConflict, "design_import_conflict", "Design import conflict", "The import command conflicts with the frozen snapshot or current target."))
	case errors.Is(err, core.ErrConflict), errors.Is(err, core.ErrProposalStale):
		problem.Write(c, problem.New(http.StatusConflict, "design_import_stale", "Design import is stale", "The PageSpec, proposal, or target Prototype changed. Refresh before retrying."))
	default:
		writeBusinessProblem(c, err)
	}
}
