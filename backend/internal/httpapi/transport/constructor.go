package transport

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/constructor"
)

const defaultConstructorMaxJSONBodyBytes int64 = 1 << 20

// ConstructorAPI exposes the immutable Application Build Contract boundary.
// Callers may choose an exact FullStackTemplate, but all source and release
// facts are resolved by the service from canonical storage.
type ConstructorAPI interface {
	CompileForManifest(context.Context, string, string, constructor.CompileForManifestInput) (constructor.ApplicationBuildContract, error)
	GetForManifest(context.Context, string, string) (constructor.ApplicationBuildContract, error)
	Get(context.Context, string, string) (constructor.ApplicationBuildContract, error)
}

type ConstructorDependencies struct {
	Service          ConstructorAPI
	MaxJSONBodyBytes int64
}

type ConstructorHandler struct {
	service ConstructorAPI
	maxBody int64
}

// createBuildContractRequest deliberately does not reuse the service input as
// an HTTP DTO. This keeps the public selection boundary closed if trusted
// compiler inputs gain fields in a later release.
type createBuildContractRequest struct {
	FullStackTemplate exactFullStackTemplateSelection `json:"fullStackTemplate"`
}

type exactFullStackTemplateSelection struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

func NewConstructorHandler(dependencies ConstructorDependencies) (*ConstructorHandler, error) {
	if dependencies.Service == nil {
		return nil, errors.New("constructor service is required")
	}
	maxBody := dependencies.MaxJSONBodyBytes
	if maxBody <= 0 {
		maxBody = defaultConstructorMaxJSONBodyBytes
	}
	return &ConstructorHandler{service: dependencies.Service, maxBody: maxBody}, nil
}

// RegisterConstructorRoutes adds the authenticated Application Build Contract
// API. The caller supplies the platform mutation middleware (CSRF,
// idempotency capture, and durable response persistence).
func RegisterConstructorRoutes(routes gin.IRoutes, handler *ConstructorHandler, mutationMiddleware ...gin.HandlerFunc) error {
	if routes == nil || handler == nil {
		return errors.New("constructor routes and handler are required")
	}
	mutation := []gin.HandlerFunc{constructorNoStore}
	mutation = append(mutation, mutationMiddleware...)
	mutation = append(mutation, handler.createForManifest)

	routes.POST("/build-manifests/:bundleId/build-contracts", mutation...)
	routes.GET("/build-manifests/:bundleId/build-contract", constructorNoStore, handler.getForManifest)
	routes.GET("/application-build-contracts/:contractId", constructorNoStore, handler.get)
	return nil
}

func constructorNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func (h *ConstructorHandler) createForManifest(c *gin.Context) {
	var request createBuildContractRequest
	if err := DecodeJSON(c, &request, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	input := constructor.CompileForManifestInput{FullStackTemplate: constructor.FullStackTemplateSelection{
		ID: request.FullStackTemplate.ID, ContentHash: request.FullStackTemplate.ContentHash,
	}}
	result, err := h.service.CompileForManifest(c.Request.Context(), c.Param("bundleId"), actor, input)
	if err != nil {
		writeBusinessProblem(c, err)
		return
	}
	// A blocked contract is still an immutable, inspectable compilation result.
	// The status is a release/generation gate, not an HTTP creation failure.
	c.Header("ETag", result.ETag)
	c.Header("Location", "/v1/application-build-contracts/"+result.ID)
	c.JSON(http.StatusCreated, result)
}

func (h *ConstructorHandler) getForManifest(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.GetForManifest(c.Request.Context(), c.Param("bundleId"), actor)
	if err != nil {
		writeBusinessProblem(c, err)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func (h *ConstructorHandler) get(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.Get(c.Request.Context(), c.Param("contractId"), actor)
	if err != nil {
		writeBusinessProblem(c, err)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}
