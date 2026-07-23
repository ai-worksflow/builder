package transport

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/automation"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

func (s *Server) AdvanceOutputProposal(context *gin.Context) {
	if s.services.Automation == nil {
		serviceUnavailable(context, "proposal automation")
		return
	}
	var input automation.AdvanceProposalInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	result, err := s.services.Automation.AdvanceProposal(
		context.Request.Context(),
		context.Param("proposalId"),
		actor,
		input,
	)
	if err != nil {
		if errors.Is(err, core.ErrBlockingGate) {
			problem.Write(context, problem.New(
				http.StatusUnprocessableEntity,
				"automation_preflight_failed",
				"Automation preflight failed",
				err.Error(),
			))
			return
		}
		s.businessError(context, err)
		return
	}
	context.Header("Cache-Control", "no-store")
	context.JSON(http.StatusOK, result)
}
