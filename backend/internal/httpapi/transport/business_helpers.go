package transport

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

const (
	defaultPageLimit = 50
	maximumPageLimit = 200
)

func actorID(context *gin.Context) (string, bool) {
	identity, ok := worksmiddleware.GetIdentity(context)
	if !ok || strings.TrimSpace(identity.Session.User.ID) == "" {
		problem.WriteError(context, auth.ErrSessionExpired)
		return "", false
	}
	return identity.Session.User.ID, true
}

func (s *Server) businessError(context *gin.Context, err error) {
	requestID := worksmiddleware.GetRequestID(context)
	if s.logger != nil {
		s.writeServiceError(requestID, context.FullPath(), err)
	}
	writeBusinessProblem(context, err)
}

func writeBusinessProblem(context *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		problem.Write(context, problem.New(http.StatusNotFound, "not_found", "Resource not found", "The requested resource was not found."))
	case errors.Is(err, domain.ErrManifestUnpinned):
		details := problem.New(http.StatusUnprocessableEntity, "manifest_unpinned", "Manifest input is not pinned", "Every manifest source must identify an immutable artifact revision and content hash.")
		addDomainErrors(&details, err)
		problem.Write(context, details)
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, domain.ErrValidation):
		details := problem.New(http.StatusUnprocessableEntity, "invalid_input", "Input is invalid", "One or more input values are invalid.")
		addDomainErrors(&details, err)
		problem.Write(context, details)
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrInvalidTransition), errors.Is(err, domain.ErrImmutable), errors.Is(err, domain.ErrStaleProposal):
		problem.Write(context, problem.New(http.StatusConflict, "conflict", "Resource conflict", "The resource changed or conflicts with the requested operation."))
	case errors.Is(err, domain.ErrSelfApproval):
		problem.Write(context, problem.New(http.StatusConflict, "self_approval", "Self approval is not allowed", "Authors cannot approve their own revision."))
	default:
		problem.WriteError(context, err)
	}
}

func addDomainErrors(details *problem.Details, err error) {
	var domainError *domain.DomainError
	if errors.As(err, &domainError) && domainError.Field != "" {
		details.Errors = map[string][]string{domainError.Field: {domainError.Message}}
		return
	}
	var validationError *domain.ValidationError
	if errors.As(err, &validationError) {
		details.Errors = map[string][]string{}
		for _, issue := range validationError.Issues {
			details.Errors[issue.Path] = append(details.Errors[issue.Path], issue.Message)
		}
	}
}

func serviceUnavailable(context *gin.Context, name string) {
	problem.Write(context, problem.New(
		http.StatusServiceUnavailable,
		"service_unavailable",
		"Service unavailable",
		fmt.Sprintf("The %s service is not configured.", name),
	))
}

func preconditionFailed(context *gin.Context, resource string) {
	problem.Write(context, problem.New(
		http.StatusPreconditionFailed,
		"etag_mismatch",
		"Precondition failed",
		"The "+resource+" changed since it was loaded.",
	))
}

func conditionalServiceError(s *Server, context *gin.Context, resource string, err error) {
	if errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrProposalStale) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrStaleProposal) {
		preconditionFailed(context, resource)
		return
	}
	s.businessError(context, err)
}

func matchETag(context *gin.Context, current, resource string) bool {
	if worksmiddleware.IfMatch(context) == current {
		return true
	}
	preconditionFailed(context, resource)
	return false
}

func entityVersionETag(kind, id string, version uint64) string {
	return fmt.Sprintf(`"%s:%s:%d"`, kind, id, version)
}

func entityHashETag(kind, id, hash string) string {
	return fmt.Sprintf(`"%s:%s:%s"`, kind, id, hash)
}

func revisionETag(revision core.ArtifactRevision) string {
	return entityHashETag("revision", revision.ID, revision.ContentHash+":"+revision.WorkflowStatus)
}

func representationETag(kind, id string, value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return entityVersionETag(kind, id, 1)
	}
	digest := sha256.Sum256(payload)
	return entityHashETag(kind, id, hex.EncodeToString(digest[:]))
}

func parsePage(context *gin.Context, length int) (int, int, bool) {
	limit := defaultPageLimit
	if value := strings.TrimSpace(context.Query("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maximumPageLimit {
			problem.Write(context, problem.New(http.StatusBadRequest, "invalid_page_limit", "Invalid page limit", "limit must be an integer from 1 to 200."))
			return 0, 0, false
		}
		limit = parsed
	}
	offset := 0
	if value := strings.TrimSpace(context.Query("cursor")); value != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || !strings.HasPrefix(string(decoded), "offset:") {
			problem.Write(context, problem.New(http.StatusBadRequest, "invalid_cursor", "Invalid cursor", "cursor is not a valid pagination cursor."))
			return 0, 0, false
		}
		parsed, parseErr := strconv.Atoi(strings.TrimPrefix(string(decoded), "offset:"))
		if parseErr != nil || parsed < 0 || parsed > length {
			problem.Write(context, problem.New(http.StatusBadRequest, "invalid_cursor", "Invalid cursor", "cursor is outside the available result set."))
			return 0, 0, false
		}
		offset = parsed
	}
	return offset, limit, true
}

func writePage[T any](context *gin.Context, items []T) bool {
	if items == nil {
		items = []T{}
	}
	offset, limit, ok := parsePage(context, len(items))
	if !ok {
		return false
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	page := items[offset:end]
	response := gin.H{"items": page, "total": len(items)}
	if end < len(items) {
		response["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte("offset:" + strconv.Itoa(end)))
	}
	context.JSON(http.StatusOK, response)
	return true
}

func optionalBoolQuery(context *gin.Context, key string) (bool, bool) {
	value := strings.TrimSpace(context.Query(key))
	if value == "" {
		return false, true
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_query", "Invalid query parameter", key+" must be true or false."))
		return false, false
	}
	return parsed, true
}
