package transport

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	"github.com/worksflow/builder/backend/internal/templates"
)

// TemplateRegistryDependencies contains the read-only registry boundary. The
// HTTP surface deliberately has no admission or release-policy mutation
// dependency, so those capabilities cannot be registered accidentally.
type TemplateRegistryDependencies struct {
	Registry templates.RegistryReader
}

type TemplateRegistryHandler struct {
	registry templates.RegistryReader
}

func NewTemplateRegistryHandler(dependencies TemplateRegistryDependencies) (*TemplateRegistryHandler, error) {
	if dependencies.Registry == nil {
		return nil, errors.New("template registry is required")
	}
	return &TemplateRegistryHandler{registry: dependencies.Registry}, nil
}

// RegisterTemplateRegistryRoutes installs authenticated, read-only registry
// routes. Authentication is supplied by the protected router group; the
// handlers also require the resulting identity as a defense against accidental
// registration on a public group.
func RegisterTemplateRegistryRoutes(routes gin.IRoutes, handler *TemplateRegistryHandler) error {
	if routes == nil || handler == nil {
		return errors.New("template registry routes and handler are required")
	}
	routes.GET("/template-releases", templateRegistryNoStore, handler.listTemplateReleases)
	routes.GET("/template-releases/:releaseId", templateRegistryNoStore, handler.getTemplateRelease)
	routes.GET("/full-stack-templates", templateRegistryNoStore, handler.listFullStackTemplates)
	routes.GET("/full-stack-templates/:templateId", templateRegistryNoStore, handler.getFullStackTemplate)
	return nil
}

func templateRegistryNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func (h *TemplateRegistryHandler) listTemplateReleases(c *gin.Context) {
	if _, ok := actorID(c); !ok {
		return
	}
	values, ok := parseTemplateRegistryQuery(c, "templateId", "limit", "state")
	if !ok {
		return
	}
	templateID, ok := optionalTemplateRegistryQuery(c, values, "templateId")
	if !ok {
		return
	}
	limit, ok := templateRegistryLimit(c, values)
	if !ok {
		return
	}
	states, ok := templateRegistryStates(c, values)
	if !ok {
		return
	}
	result, err := h.registry.ListTemplateReleases(c.Request.Context(), templates.TemplateReleaseListOptions{
		TemplateID: templateID,
		States:     states,
		Limit:      limit,
	})
	if err != nil {
		writeTemplateRegistryProblem(c, err)
		return
	}
	if result == nil {
		result = []templates.TemplateReleaseRegistration{}
	}
	c.JSON(http.StatusOK, gin.H{"items": result})
}

func (h *TemplateRegistryHandler) getTemplateRelease(c *gin.Context) {
	if _, ok := actorID(c); !ok {
		return
	}
	values, ok := parseTemplateRegistryQuery(c, "contentHash", "subjectHash")
	if !ok {
		return
	}
	contentHash, contentExact, ok := exactTemplateRegistryQuery(c, values, "contentHash")
	if !ok {
		return
	}
	subjectHash, subjectExact, ok := exactTemplateRegistryQuery(c, values, "subjectHash")
	if !ok {
		return
	}
	if contentExact != subjectExact {
		writeTemplateRegistryInvalidQuery(c, "contentHash and subjectHash must be provided together for an exact template release lookup.")
		return
	}
	var (
		result templates.TemplateReleaseRegistration
		err    error
	)
	if contentExact {
		result, err = h.registry.GetTemplateReleaseExact(c.Request.Context(), templates.TemplateReleaseRef{
			ID:          c.Param("releaseId"),
			ContentHash: contentHash,
			SubjectHash: subjectHash,
		})
	} else {
		result, err = h.registry.GetTemplateRelease(c.Request.Context(), c.Param("releaseId"))
	}
	if err != nil {
		writeTemplateRegistryProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *TemplateRegistryHandler) listFullStackTemplates(c *gin.Context) {
	if _, ok := actorID(c); !ok {
		return
	}
	values, ok := parseTemplateRegistryQuery(c, "templateId", "limit")
	if !ok {
		return
	}
	templateID, ok := optionalTemplateRegistryQuery(c, values, "templateId")
	if !ok {
		return
	}
	limit, ok := templateRegistryLimit(c, values)
	if !ok {
		return
	}
	result, err := h.registry.ListFullStackTemplates(c.Request.Context(), templates.FullStackTemplateListOptions{
		TemplateID: templateID,
		Limit:      limit,
	})
	if err != nil {
		writeTemplateRegistryProblem(c, err)
		return
	}
	if result == nil {
		result = []templates.FullStackTemplateRegistration{}
	}
	c.JSON(http.StatusOK, gin.H{"items": result})
}

func (h *TemplateRegistryHandler) getFullStackTemplate(c *gin.Context) {
	if _, ok := actorID(c); !ok {
		return
	}
	values, ok := parseTemplateRegistryQuery(c, "contentHash")
	if !ok {
		return
	}
	contentHash, exact, ok := exactTemplateRegistryQuery(c, values, "contentHash")
	if !ok {
		return
	}
	var (
		result templates.FullStackTemplateRegistration
		err    error
	)
	if exact {
		result, err = h.registry.GetFullStackTemplateExact(c.Request.Context(), templates.ExactFullStackTemplateRef{
			ID: c.Param("templateId"), ContentHash: contentHash,
		})
	} else {
		result, err = h.registry.GetFullStackTemplate(c.Request.Context(), c.Param("templateId"))
	}
	if err != nil {
		writeTemplateRegistryProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func parseTemplateRegistryQuery(c *gin.Context, allowed ...string) (url.Values, bool) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeTemplateRegistryInvalidQuery(c, "The query string is malformed.")
		return nil, false
	}
	accepted := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		accepted[key] = struct{}{}
	}
	for key := range values {
		if _, ok := accepted[key]; !ok {
			writeTemplateRegistryInvalidQuery(c, "Unknown query parameter: "+key+".")
			return nil, false
		}
	}
	return values, true
}

func optionalTemplateRegistryQuery(c *gin.Context, values url.Values, key string) (string, bool) {
	value, present, ok := exactTemplateRegistryQuery(c, values, key)
	if !ok {
		return "", false
	}
	if !present {
		return "", true
	}
	return value, true
}

func exactTemplateRegistryQuery(c *gin.Context, values url.Values, key string) (string, bool, bool) {
	entries, present := values[key]
	if !present {
		return "", false, true
	}
	if len(entries) != 1 {
		writeTemplateRegistryInvalidQuery(c, key+" must be provided at most once.")
		return "", false, false
	}
	value := strings.TrimSpace(entries[0])
	if value == "" || value != entries[0] {
		writeTemplateRegistryInvalidQuery(c, key+" must be a non-empty canonical value.")
		return "", false, false
	}
	return value, true, true
}

func templateRegistryLimit(c *gin.Context, values url.Values) (int, bool) {
	value, present, ok := exactTemplateRegistryQuery(c, values, "limit")
	if !ok {
		return 0, false
	}
	if !present {
		return 0, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 100 || strconv.Itoa(limit) != value {
		writeTemplateRegistryInvalidQuery(c, "limit must be an integer from 1 to 100.")
		return 0, false
	}
	return limit, true
}

func templateRegistryStates(c *gin.Context, values url.Values) ([]templates.ReleasePolicyState, bool) {
	entries, present := values["state"]
	if !present {
		return nil, true
	}
	states := make([]templates.ReleasePolicyState, 0, len(entries))
	for _, entry := range entries {
		if entry == "" || entry != strings.TrimSpace(entry) {
			writeTemplateRegistryInvalidQuery(c, "state must be approved, deprecated, or revoked.")
			return nil, false
		}
		state := templates.ReleasePolicyState(entry)
		if state != templates.ReleaseApproved && state != templates.ReleaseDeprecated && state != templates.ReleaseRevoked {
			writeTemplateRegistryInvalidQuery(c, "state must be approved, deprecated, or revoked.")
			return nil, false
		}
		states = append(states, state)
	}
	return states, true
}

func writeTemplateRegistryInvalidQuery(c *gin.Context, detail string) {
	problem.Write(c, problem.New(http.StatusBadRequest, "invalid_query", "Invalid query parameter", detail))
}

func writeTemplateRegistryProblem(c *gin.Context, err error) {
	switch {
	case errors.Is(err, templates.ErrInvalidTemplate):
		details := problem.New(http.StatusBadRequest, "invalid_query", "Invalid query parameter", "One or more template registry query values are invalid.")
		var templateError *templates.Error
		if errors.As(err, &templateError) && templateError.Field != "" {
			details.Errors = map[string][]string{templateError.Field: {templateError.Detail}}
		}
		problem.Write(c, details)
	case errors.Is(err, templates.ErrRegistryNotFound):
		problem.Write(c, problem.New(http.StatusNotFound, "not_found", "Resource not found", "The requested template registry resource was not found."))
	case errors.Is(err, templates.ErrRegistryIntegrity):
		problem.Write(c, problem.New(http.StatusInternalServerError, "template_registry_integrity", "Template registry integrity check failed", "Stored template registry data failed its canonical integrity checks."))
	case errors.Is(err, templates.ErrRegistryUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "template_registry_unavailable", "Template registry unavailable", "The template registry is temporarily unavailable."))
	default:
		problem.WriteError(c, err)
	}
}
