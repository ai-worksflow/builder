package workflow

import (
	"strings"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type ReviewDecision struct {
	Resolution              ReviewResolution
	Reason                  string
	Actor                   ActorProvenance
	ExecutionAuthorizations map[string]ActorProvenance
}

func nodeExecutionPolicy(node domain.NodeDefinition) (core.Action, core.Role, bool) {
	switch node.Type {
	case domain.NodeQualityGate:
		role := core.RoleEditor
		if node.QualityGate != nil && strings.TrimSpace(node.QualityGate.RequiredRole) != "" {
			role = core.Role(strings.TrimSpace(node.QualityGate.RequiredRole))
		}
		return core.ActionEdit, role, true
	case domain.NodePublish:
		if node.Publish == nil {
			return "", "", false
		}
		return core.ActionPublish, core.Role(strings.TrimSpace(node.Publish.RequiredRole)), true
	default:
		return "", "", false
	}
}

func provenanceEventPayload(actor ActorProvenance) map[string]any {
	return map[string]any{
		"role": actor.Role, "action": actor.Action, "source": actor.Source,
		"authorizedAt": actor.AuthorizedAt.UTC(),
	}
}
