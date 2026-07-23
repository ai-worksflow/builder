package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

// ValidateDefinitionForExecutionProfile is the single definition-validation
// seam for persistence, authoring, discovery, start and runtime replay. The
// exact persisted profile selects a frozen validator; callers may not supply a
// separate capability registry or analysis limit.
func ValidateDefinitionForExecutionProfile(definition domain.WorkflowDefinition, profile domain.WorkflowExecutionProfileRef) error {
	if err := profile.Validate(); err != nil {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: err.Error()}
	}
	switch profile {
	case WorkflowExecutionProfileV2Ref():
		return validateDefinitionV2(definition)
	case WorkflowExecutionProfileV1Ref():
		return validateDefinitionV1(definition)
	case LegacyWorkflowExecutionProfileRef():
		return validateDefinitionV0Replay(definition)
	case WorkflowExecutionProfileV3Ref():
		return validateDefinitionV3(definition)
	default:
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: "execution profile is not registered by this runtime"}
	}
}

// validateDefinitionV3 is the frozen authoring validator for the separately
// described workflow-engine/v3 profile. Runtime registration is intentionally
// independent and remains unavailable until the qualification authority and
// private handoff are sealed.
func validateDefinitionV3(definition domain.WorkflowDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	profile := WorkflowExecutionProfileV3Ref()
	if definition.ExecutionProfile != profile {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: "definition content does not embed the exact selected profile"}
	}
	descriptor := WorkflowExecutionProfileV3Descriptor()
	if err := validateDefinitionAnalysisLimitsV1(definition, descriptor.Capabilities.AnalysisLimits); err != nil {
		return err
	}
	for _, node := range definition.Nodes {
		for _, requiredRole := range workflowNodeRoles(node) {
			if !validWorkflowRole(requiredRole) {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "requiredRole is not a project role"}
			}
		}
		if node.Condition == nil {
			continue
		}
		for _, branch := range node.Condition.Branches {
			if branch.Default {
				continue
			}
			if len([]byte(branch.Expression)) > descriptor.Capabilities.AnalysisLimits.MaximumConditionExpression {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "condition expression is too large"}
			}
			if _, err := evaluateConditionRule(json.RawMessage(branch.Expression), map[string]any{}, 0); err != nil {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: err.Error()}
			}
		}
	}
	if err := descriptor.Capabilities.ValidateDefinition(definition); err != nil {
		return err
	}
	return validateExternalQualificationTopologyV3(definition)
}

func validateExternalQualificationTopologyV3(definition domain.WorkflowDefinition) error {
	var workbench, releaseQuality, external, publish *domain.NodeDefinition
	incoming := make(map[string][]domain.WorkflowEdge, len(definition.Nodes))
	outgoing := make(map[string][]domain.WorkflowEdge, len(definition.Nodes))
	for index := range definition.Nodes {
		node := &definition.Nodes[index]
		switch node.Type {
		case domain.NodeWorkbenchBuild:
			if workbench != nil {
				return profileV3TopologyError("workflow.nodes", "profile v3 requires exactly one Workbench node")
			}
			workbench = node
		case domain.NodeQualityGate:
			if node.QualityGate == nil || node.QualityGate.GateName != "release" || !node.QualityGate.Blocking {
				return profileV3TopologyError("workflow.nodes."+node.ID+".qualityGate", "profile v3 permits only the blocking release quality gate")
			}
			if releaseQuality != nil {
				return profileV3TopologyError("workflow.nodes", "profile v3 requires exactly one blocking release quality gate")
			}
			releaseQuality = node
		case domain.NodeExternalQualificationGate:
			if external != nil {
				return profileV3TopologyError("workflow.nodes", "profile v3 requires exactly one dedicated external qualification gate")
			}
			if node.ID != "external-qualification" {
				return profileV3TopologyError("workflow.nodes."+node.ID, "profile v3 requires the dedicated gate id external-qualification")
			}
			if node.ExternalQualificationGate == nil || !node.ExternalQualificationGate.IsExact() {
				return profileV3TopologyError("workflow.nodes."+node.ID+".externalQualificationGate", "profile v3 requires the exact closed non-waivable gate config")
			}
			external = node
		case domain.NodePublish:
			if publish != nil {
				return profileV3TopologyError("workflow.nodes", "profile v3 requires exactly one Publish node")
			}
			if node.Publish == nil || node.Publish.Environment != "production" {
				return profileV3TopologyError("workflow.nodes."+node.ID+".publish", "profile v3 requires production Publish")
			}
			publish = node
		}
	}
	for _, edge := range definition.Edges {
		incoming[edge.To] = append(incoming[edge.To], edge)
		outgoing[edge.From] = append(outgoing[edge.From], edge)
	}
	if workbench == nil || releaseQuality == nil || external == nil || publish == nil {
		return profileV3TopologyError("workflow.nodes", "profile v3 requires exactly Workbench -> blocking release QualityGate -> external qualification gate -> production Publish")
	}
	if !hasExactProfileV3Edge(outgoing[workbench.ID], workbench.ID, releaseQuality.ID) ||
		!hasExactProfileV3Edge(incoming[releaseQuality.ID], workbench.ID, releaseQuality.ID) {
		return profileV3TopologyError("workflow.nodes."+workbench.ID, "Workbench must flow only and directly to the blocking release QualityGate")
	}
	if !hasExactProfileV3Edge(outgoing[releaseQuality.ID], releaseQuality.ID, external.ID) ||
		!hasExactProfileV3Edge(incoming[external.ID], releaseQuality.ID, external.ID) {
		return profileV3TopologyError("workflow.nodes."+releaseQuality.ID, "the blocking release QualityGate must flow only and directly to external qualification")
	}
	if !hasExactProfileV3Edge(outgoing[external.ID], external.ID, publish.ID) ||
		!hasExactProfileV3Edge(incoming[publish.ID], external.ID, publish.ID) {
		return profileV3TopologyError("workflow.nodes."+external.ID, "external qualification must flow only and directly to production Publish")
	}
	if len(outgoing[publish.ID]) != 0 {
		return profileV3TopologyError("workflow.nodes."+publish.ID, "production Publish must be terminal")
	}
	if profileV3PathExistsWithoutGate(definition, external.ID, publish.ID) {
		return profileV3TopologyError("workflow.nodes."+publish.ID, "every successful terminal path must cross the dedicated external qualification gate")
	}
	return nil
}

func hasExactProfileV3Edge(edges []domain.WorkflowEdge, fromNodeID, toNodeID string) bool {
	if len(edges) != 1 {
		return false
	}
	edge := edges[0]
	return edge.From == fromNodeID && edge.To == toNodeID &&
		(edge.FromPort == "" || edge.FromPort == "default") &&
		(edge.ToPort == "" || edge.ToPort == "default") && len(edge.Mapping) == 0
}

func profileV3PathExistsWithoutGate(definition domain.WorkflowDefinition, gateID, terminalID string) bool {
	entryID, err := definition.EntryNodeID()
	if err != nil || entryID == gateID {
		return false
	}
	adjacency := make(map[string][]string, len(definition.Nodes))
	for _, edge := range definition.Edges {
		if edge.From == gateID || edge.To == gateID {
			continue
		}
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}
	visited := map[string]bool{}
	queue := []string{entryID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == terminalID {
			return true
		}
		if visited[current] {
			continue
		}
		visited[current] = true
		queue = append(queue, adjacency[current]...)
	}
	return false
}

func profileV3TopologyError(field, message string) error {
	return &domain.DomainError{Kind: domain.ErrValidation, Field: field, Message: message}
}

// validateDefinitionV0Replay preserves the pre-pin acceptance contract. It is
// intentionally independent from v1 authoring policy. Production starts reject
// this profile; generic isolated engines may opt into it for legacy fixtures,
// and already-persisted legacy runs remain replayable.
func validateDefinitionV0Replay(definition domain.WorkflowDefinition) error {
	if !definition.ExecutionProfile.IsZero() && definition.ExecutionProfile != LegacyWorkflowExecutionProfileRef() {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: "legacy replay definition has a different embedded profile"}
	}
	return definition.Validate()
}

// validateDefinitionV1 is frozen by workflow-engine/v1. Future authoring
// behavior must add validateDefinitionV2 and a new descriptor; it must not edit
// this validator after v1 definitions have been issued.
func validateDefinitionV1(definition domain.WorkflowDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	profile := WorkflowExecutionProfileV1Ref()
	if definition.ExecutionProfile != profile {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: "definition content does not embed the exact selected profile"}
	}
	descriptor := WorkflowExecutionProfileV1Descriptor()
	if err := validateDefinitionAnalysisLimitsV1(definition, descriptor.Capabilities.AnalysisLimits); err != nil {
		return err
	}
	for _, node := range definition.Nodes {
		for _, requiredRole := range workflowNodeRoles(node) {
			if !validWorkflowRole(requiredRole) {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "requiredRole is not a project role"}
			}
		}
		if node.Condition == nil {
			continue
		}
		for _, branch := range node.Condition.Branches {
			if branch.Default {
				continue
			}
			if len([]byte(branch.Expression)) > descriptor.Capabilities.AnalysisLimits.MaximumConditionExpression {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "condition expression is too large"}
			}
			if _, err := evaluateConditionRule(json.RawMessage(branch.Expression), map[string]any{}, 0); err != nil {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: err.Error()}
			}
		}
	}
	return descriptor.Capabilities.ValidateDefinition(definition)
}

// validateDefinitionV2 is frozen by workflow-engine/v2. Current authoring
// aliases this entry point until a separately versioned profile exists.
func validateDefinitionV2(definition domain.WorkflowDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	profile := WorkflowExecutionProfileV2Ref()
	if definition.ExecutionProfile != profile {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: "definition content does not embed the exact selected profile"}
	}
	descriptor := WorkflowExecutionProfileV2Descriptor()
	if err := validateDefinitionAnalysisLimitsV1(definition, descriptor.Capabilities.AnalysisLimits); err != nil {
		return err
	}
	for _, node := range definition.Nodes {
		for _, requiredRole := range workflowNodeRoles(node) {
			if !validWorkflowRole(requiredRole) {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "requiredRole is not a project role"}
			}
		}
		if node.Condition == nil {
			continue
		}
		for _, branch := range node.Condition.Branches {
			if branch.Default {
				continue
			}
			if len([]byte(branch.Expression)) > descriptor.Capabilities.AnalysisLimits.MaximumConditionExpression {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "condition expression is too large"}
			}
			if _, err := evaluateConditionRule(json.RawMessage(branch.Expression), map[string]any{}, 0); err != nil {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: err.Error()}
			}
		}
	}
	return descriptor.Capabilities.ValidateDefinition(definition)
}

func validateDefinitionAnalysisLimitsV1(definition domain.WorkflowDefinition, limits WorkflowAnalysisLimits) error {
	if limits.MaximumDefinitionNodes < 1 || limits.MaximumDefinitionEdges < 0 || limits.MaxSemanticPathStates < 1 || limits.MaximumConditionExpression < 1 {
		return fmt.Errorf("workflow execution profile contains invalid analysis limits")
	}
	if len(definition.Nodes) > limits.MaximumDefinitionNodes {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes", Message: fmt.Sprintf("workflow exceeds the execution profile node limit of %d", limits.MaximumDefinitionNodes)}
	}
	if len(definition.Edges) > limits.MaximumDefinitionEdges {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.edges", Message: fmt.Sprintf("workflow exceeds the execution profile edge limit of %d", limits.MaximumDefinitionEdges)}
	}
	return nil
}

func workflowNodeRoles(node domain.NodeDefinition) []string {
	roles := make([]string, 0, 1)
	if node.HumanEdit != nil {
		roles = append(roles, node.HumanEdit.RequiredRole)
	}
	if node.HumanTask != nil {
		roles = append(roles, node.HumanTask.RequiredRole)
	}
	if node.ReviewGate != nil {
		roles = append(roles, node.ReviewGate.RequiredRole)
	}
	if node.Approval != nil {
		roles = append(roles, node.Approval.RequiredRole)
	}
	if node.QualityGate != nil && strings.TrimSpace(node.QualityGate.RequiredRole) != "" {
		roles = append(roles, node.QualityGate.RequiredRole)
	}
	if node.Publish != nil {
		roles = append(roles, node.Publish.RequiredRole)
	}
	return roles
}

func validWorkflowRole(role string) bool {
	switch core.Role(strings.TrimSpace(role)) {
	case core.RoleOwner, core.RoleAdmin, core.RoleEditor, core.RoleCommenter, core.RoleViewer:
		return true
	default:
		return false
	}
}
