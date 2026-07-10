package transport

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type addMemberInput struct {
	Email       string    `json:"email"`
	DisplayName *string   `json:"displayName,omitempty"`
	Role        core.Role `json:"role"`
}

type updateMemberInput struct {
	Role core.Role `json:"role"`
}

type invitationInput struct {
	Email string    `json:"email"`
	Role  core.Role `json:"role"`
}

type acceptInvitationInput struct {
	Token string `json:"token"`
}

func (s *Server) ListMembers(context *gin.Context) {
	identity, _ := worksmiddleware.GetIdentity(context)
	members, err := s.services.Members.List(context.Request.Context(), context.Param("projectId"), identity.Session.User.ID)
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.JSON(http.StatusOK, gin.H{"items": members, "total": len(members)})
}

func (s *Server) AddMember(context *gin.Context) {
	var input addMemberInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	identity, _ := worksmiddleware.GetIdentity(context)
	member, err := s.services.Members.AddExisting(context.Request.Context(), context.Param("projectId"), identity.Session.User.ID, input.Email, input.Role)
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.Header("ETag", member.ETag)
	context.JSON(http.StatusCreated, member)
}

func (s *Server) UpdateMember(context *gin.Context) {
	var input updateMemberInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	identity, _ := worksmiddleware.GetIdentity(context)
	member, err := s.services.Members.UpdateRole(
		context.Request.Context(), context.Param("projectId"), identity.Session.User.ID,
		context.Param("userId"), input.Role, worksmiddleware.IfMatch(context),
	)
	if errors.Is(err, core.ErrConflict) {
		problem.Write(context, problem.New(http.StatusPreconditionFailed, "etag_mismatch", "Precondition failed", "The membership changed since it was loaded."))
		return
	}
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.Header("ETag", member.ETag)
	context.JSON(http.StatusOK, member)
}

func (s *Server) RemoveMember(context *gin.Context) {
	identity, _ := worksmiddleware.GetIdentity(context)
	err := s.services.Members.Remove(
		context.Request.Context(), context.Param("projectId"), identity.Session.User.ID,
		context.Param("userId"), worksmiddleware.IfMatch(context),
	)
	if errors.Is(err, core.ErrConflict) {
		problem.Write(context, problem.New(http.StatusPreconditionFailed, "etag_mismatch", "Precondition failed", "The membership changed since it was loaded."))
		return
	}
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.Status(http.StatusNoContent)
}

func (s *Server) CreateInvitation(context *gin.Context) {
	noStore(context)
	var input invitationInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	identity, _ := worksmiddleware.GetIdentity(context)
	invitation, err := s.services.Members.Invite(context.Request.Context(), context.Param("projectId"), identity.Session.User.ID, input.Email, input.Role)
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.JSON(http.StatusCreated, invitation)
}

func (s *Server) AcceptInvitation(context *gin.Context) {
	var input acceptInvitationInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	identity, _ := worksmiddleware.GetIdentity(context)
	member, err := s.services.Members.AcceptInvitation(context.Request.Context(), identity.Session.User.ID, input.Token)
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.Header("ETag", member.ETag)
	context.JSON(http.StatusOK, member)
}
