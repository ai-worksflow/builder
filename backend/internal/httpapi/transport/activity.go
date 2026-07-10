package transport

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

func (s *Server) ListNotifications(context *gin.Context) {
	if s.services.Activity == nil {
		serviceUnavailable(context, "activity")
		return
	}
	unread, ok := optionalBoolQuery(context, "unread")
	if !ok {
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Activity.ListNotifications(
		context.Request.Context(), actor, strings.TrimSpace(context.Query("projectId")), unread,
	)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, items)
}

func (s *Server) MarkNotification(context *gin.Context) {
	if s.services.Activity == nil {
		serviceUnavailable(context, "activity")
		return
	}
	var input struct {
		Read *bool `json:"read"`
	}
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	if input.Read == nil {
		s.businessError(context, core.ErrInvalidInput)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	notification, err := s.services.Activity.MarkNotification(
		context.Request.Context(), context.Param("notificationId"), actor, worksmiddleware.IfMatch(context), *input.Read,
	)
	if err != nil {
		conditionalServiceError(s, context, "notification", err)
		return
	}
	context.Header("ETag", notification.ETag)
	context.JSON(http.StatusOK, notification)
}

func (s *Server) ListAuditEvents(context *gin.Context) {
	if s.services.Activity == nil {
		serviceUnavailable(context, "activity")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Activity.ListAudit(context.Request.Context(), context.Param("projectId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, items)
}

func (s *Server) ListPresence(context *gin.Context) {
	if s.services.Activity == nil {
		serviceUnavailable(context, "activity")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Activity.ListPresence(context.Request.Context(), context.Param("projectId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, items)
}

func (s *Server) HeartbeatPresence(context *gin.Context) {
	if s.services.Activity == nil {
		serviceUnavailable(context, "activity")
		return
	}
	var input struct {
		ArtifactID *string `json:"artifactId,omitempty"`
	}
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	presence, err := s.services.Activity.HeartbeatPresence(context.Request.Context(), context.Param("projectId"), actor, input.ArtifactID)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.JSON(http.StatusOK, presence)
}
