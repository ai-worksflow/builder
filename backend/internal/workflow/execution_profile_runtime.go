package workflow

import (
	"context"

	"github.com/worksflow/builder/backend/internal/domain"
)

// The v0, v1 and v2 entry points below are intentionally permanent and
// separate. A future behavior change adds v3 functions and a new descriptor;
// it never redirects any historical profile to newer interpreter code.

func sealRuntimeV0(bootstrap workflowExecutionRuntime, descriptor WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error) {
	if descriptor.Components != LegacyWorkflowExecutionProfileDescriptor().Components {
		return workflowExecutionRuntime{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.executionProfile", Message: "legacy runtime component identity drifted"}
	}
	runtime := cloneExecutionRuntime(bootstrap)
	runtime.conditionEvaluator = DeclarativeConditionEvaluator{}
	return runtime, nil
}

func sealRuntimeV1(bootstrap workflowExecutionRuntime, descriptor WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error) {
	if descriptor.Components != WorkflowExecutionProfileV1Descriptor().Components {
		return workflowExecutionRuntime{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.executionProfile", Message: "workflow-engine/v1 runtime component identity drifted"}
	}
	runtime := cloneExecutionRuntime(bootstrap)
	runtime.conditionEvaluator = DeclarativeConditionEvaluatorV1{}
	return runtime, nil
}

func sealRuntimeV2(bootstrap workflowExecutionRuntime, descriptor WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error) {
	if descriptor.Components != WorkflowExecutionProfileV2Descriptor().Components {
		return workflowExecutionRuntime{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.executionProfile", Message: "workflow-engine/v2 runtime component identity drifted"}
	}
	runtime := cloneExecutionRuntime(bootstrap)
	runtime.conditionEvaluator = DeclarativeConditionEvaluatorV1{}
	return runtime, nil
}

func buildNodeInputEnvelopeV0(run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord) (domain.NodeInputEnvelope, error) {
	legacyRun := *run
	legacyRun.ExecutionProfile = domain.WorkflowExecutionProfileRef{}
	legacyRun.Definition.ExecutionProfile = domain.WorkflowExecutionProfileRef{}
	definition.ExecutionProfile = domain.WorkflowExecutionProfileRef{}
	return buildNodeInputEnvelope(&legacyRun, definition, node)
}

func executeNodeV0(ctx context.Context, engine *Engine, runtime workflowExecutionRuntime, execution Execution) (WorkerResult, error) {
	execution.Run.ExecutionProfile = domain.WorkflowExecutionProfileRef{}
	execution.Run.Definition.ExecutionProfile = domain.WorkflowExecutionProfileRef{}
	execution.Workflow.ExecutionProfile = domain.WorkflowExecutionProfileRef{}
	execution.legacyProfileView = true
	return engine.executeNodeV0V1Frozen(ctx, runtime, execution)
}

func validateResultV0(ctx context.Context, engine *Engine, _ workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, execution Execution, result *WorkerResult) error {
	return engine.validateResultV0V1Frozen(ctx, run, definition, node, execution, result)
}

func applyResultV0(ctx context.Context, engine *Engine, runtime workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, lease Lease, execution Execution, result WorkerResult) error {
	return engine.applyResultV0V1Frozen(ctx, runtime, run, definition, node, lease, execution, result, validateResultV0, reconcileV0)
}

func reconcileV0(engine *Engine, _ workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	engine.reconcileV0V1Frozen(run, definition, builder)
}

func buildNodeInputEnvelopeV1(run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord) (domain.NodeInputEnvelope, error) {
	return buildNodeInputEnvelope(run, definition, node)
}

func executeNodeV1(ctx context.Context, engine *Engine, runtime workflowExecutionRuntime, execution Execution) (WorkerResult, error) {
	return engine.executeNodeV0V1Frozen(ctx, runtime, execution)
}

func validateResultV1(ctx context.Context, engine *Engine, _ workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, execution Execution, result *WorkerResult) error {
	return engine.validateResultV0V1Frozen(ctx, run, definition, node, execution, result)
}

func applyResultV1(ctx context.Context, engine *Engine, runtime workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, lease Lease, execution Execution, result WorkerResult) error {
	return engine.applyResultV0V1Frozen(ctx, runtime, run, definition, node, lease, execution, result, validateResultV1, reconcileV1)
}

func reconcileV1(engine *Engine, _ workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	engine.reconcileV0V1Frozen(run, definition, builder)
}

func buildNodeInputEnvelopeV2(run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord) (domain.NodeInputEnvelope, error) {
	return buildNodeInputEnvelope(run, definition, node)
}

func executeNodeV2(ctx context.Context, engine *Engine, runtime workflowExecutionRuntime, execution Execution) (WorkerResult, error) {
	return engine.executeNodeV0V1Frozen(ctx, runtime, execution)
}

func validateResultV2(ctx context.Context, engine *Engine, _ workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, execution Execution, result *WorkerResult) error {
	return engine.validateResultV0V1Frozen(ctx, run, definition, node, execution, result)
}

func applyResultV2(ctx context.Context, engine *Engine, runtime workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, lease Lease, execution Execution, result WorkerResult) error {
	return engine.applyResultV0V1Frozen(ctx, runtime, run, definition, node, lease, execution, result, validateResultV2, reconcileV2)
}

func reconcileV2(engine *Engine, _ workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	engine.reconcileV2(run, definition, builder)
}
