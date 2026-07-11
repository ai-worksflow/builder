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
	case CurrentWorkflowExecutionProfileRef():
		return validateDefinitionV2(definition)
	case WorkflowExecutionProfileV1Ref():
		return validateDefinitionV1(definition)
	case LegacyWorkflowExecutionProfileRef():
		return validateDefinitionV0Replay(definition)
	default:
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: "execution profile is not registered by this runtime"}
	}
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

// validateDefinitionV2 is the current authoring validator. It intentionally
// has its own entry point even while its acceptance rules match v1, so future
// changes cannot reinterpret definitions persisted under either exact ref.
func validateDefinitionV2(definition domain.WorkflowDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	profile := CurrentWorkflowExecutionProfileRef()
	if definition.ExecutionProfile != profile {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: "definition content does not embed the exact selected profile"}
	}
	descriptor := CurrentWorkflowExecutionProfileDescriptor()
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
