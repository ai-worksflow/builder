package problem

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/core"
)

type Details struct {
	Type       string                 `json:"type"`
	Title      string                 `json:"title"`
	Status     int                    `json:"status"`
	Detail     string                 `json:"detail,omitempty"`
	Instance   string                 `json:"instance,omitempty"`
	Code       string                 `json:"code,omitempty"`
	RequestID  string                 `json:"requestId,omitempty"`
	Errors     map[string][]string    `json:"errors,omitempty"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

func New(status int, code, title, detail string) Details {
	return Details{
		Type:   "urn:worksflow:problem:" + code,
		Title:  title,
		Status: status,
		Detail: detail,
		Code:   code,
	}
}

func Write(context *gin.Context, details Details) {
	if details.Status < 400 {
		details.Status = http.StatusInternalServerError
	}
	if details.Type == "" {
		details.Type = "about:blank"
	}
	if details.Title == "" {
		details.Title = http.StatusText(details.Status)
	}
	if details.Instance == "" && context.Request != nil && context.Request.URL != nil {
		details.Instance = context.Request.URL.Path
	}
	if details.RequestID == "" {
		details.RequestID = context.GetString("request_id")
		if details.RequestID == "" {
			details.RequestID = context.Writer.Header().Get("X-Request-ID")
		}
	}
	context.Header("Content-Type", "application/problem+json")
	context.AbortWithStatusJSON(details.Status, details)
}

func WriteError(context *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		Write(context, New(http.StatusUnauthorized, "invalid_credentials", "Authentication failed", "The email or password is incorrect."))
	case errors.Is(err, auth.ErrSessionExpired):
		Write(context, New(http.StatusUnauthorized, "authentication_required", "Authentication required", "The session is missing, expired, or revoked."))
	case errors.Is(err, auth.ErrEmailExists):
		Write(context, New(http.StatusConflict, "email_exists", "Email already registered", "An account already exists for this email."))
	case errors.Is(err, auth.ErrUserDisabled):
		Write(context, New(http.StatusForbidden, "user_disabled", "Account disabled", "This account is disabled."))
	case errors.Is(err, core.ErrNotFound):
		Write(context, New(http.StatusNotFound, "not_found", "Resource not found", "The requested resource was not found."))
	case errors.Is(err, core.ErrForbidden):
		Write(context, New(http.StatusForbidden, "forbidden", "Operation forbidden", "The current user is not permitted to perform this operation."))
	case errors.Is(err, core.ErrInvalidInput):
		Write(context, New(http.StatusUnprocessableEntity, "invalid_input", "Input is invalid", "One or more input values are invalid."))
	case errors.Is(err, core.ErrConflict), errors.Is(err, core.ErrProposalStale):
		Write(context, New(http.StatusConflict, "conflict", "Resource conflict", "The resource changed or conflicts with the requested operation."))
	case errors.Is(err, core.ErrLastOwner):
		Write(context, New(http.StatusConflict, "last_owner", "Owner required", "A project must retain at least one owner."))
	case errors.Is(err, core.ErrActiveWorkflowRuns):
		Write(context, New(http.StatusConflict, "active_workflow_runs", "Workflow run in progress", "Project governance mode cannot change while a workflow run is active."))
	case errors.Is(err, core.ErrSoloOwnerInvariant):
		Write(context, New(http.StatusConflict, "solo_owner_invariant", "Solo mode requires one owner", "Switch to team mode before adding another owner, or retain exactly one owner before enabling solo mode."))
	case errors.Is(err, core.ErrSoloReviewConfirmation):
		Write(context, New(http.StatusConflict, "solo_review_confirmation_required", "Solo self-review confirmation required", "Confirm the solo self-review and provide a review explanation."))
	case errors.Is(err, core.ErrSelfApproval):
		Write(context, New(http.StatusConflict, "self_approval", "Self approval is not allowed", "Authors cannot approve their own revision."))
	case errors.Is(err, core.ErrBlockingGate):
		Write(context, New(http.StatusConflict, "blocking_gate", "Review gate is blocked", "A blocking review gate is not satisfied."))
	case errors.Is(err, core.ErrContentNotReady):
		Write(context, New(http.StatusServiceUnavailable, "content_not_ready", "Content is not ready", "Content finalization has not completed."))
	default:
		Write(context, New(http.StatusInternalServerError, "internal_error", "Internal server error", "An unexpected error occurred."))
	}
}
