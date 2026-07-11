package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	CurrentWorkflowExecutionProfileVersion = "workflow-engine/v2"
	WorkflowExecutionProfileV1Version      = "workflow-engine/v1"
	LegacyWorkflowExecutionProfileVersion  = "legacy-pre-pin/v0"
	CurrentWorkflowExecutionProfileHash    = "dd247a77ce3cfa1095a575a238b93c4bd41dd991eac07e8b62ec170864470da1"
	WorkflowExecutionProfileV1Hash         = "648034d2edc8f82ac2b2959b89e181b8b67db80dadbfcd354672f386d81cbdc1"
	LegacyWorkflowExecutionProfileHash     = "bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c"
)

// WorkflowExecutionComponents names every version-sensitive seam involved in
// interpreting and advancing a run. Changing any ID or capability/limit in the
// descriptor requires a new profile hash (and normally a new version).
type WorkflowExecutionComponents struct {
	CoreInterpreterID   string `json:"coreInterpreterId"`
	InputBuilderID      string `json:"inputBuilderId"`
	ResultValidatorID   string `json:"resultValidatorId"`
	ResultApplyID       string `json:"resultApplyId"`
	ReconcileID         string `json:"reconcileId"`
	RunnerDispatchID    string `json:"runnerDispatchId"`
	ManifestCompilerID  string `json:"manifestCompilerId"`
	ConditionAnalysisID string `json:"conditionAnalysisId"`
	ProposalAnalysisID  string `json:"proposalAnalysisId"`
}

type WorkflowExecutionProfileDescriptor struct {
	Version      string                      `json:"version"`
	Capabilities WorkflowCapabilities        `json:"capabilities"`
	Components   WorkflowExecutionComponents `json:"components"`
}

func (d WorkflowExecutionProfileDescriptor) Ref() (domain.WorkflowExecutionProfileRef, error) {
	hash, err := domain.CanonicalHash(d)
	if err != nil {
		return domain.WorkflowExecutionProfileRef{}, err
	}
	ref := domain.WorkflowExecutionProfileRef{Version: d.Version, Hash: hash}
	return ref, ref.Validate()
}

func CurrentWorkflowExecutionProfileDescriptor() WorkflowExecutionProfileDescriptor {
	return WorkflowExecutionProfileDescriptor{
		Version:      CurrentWorkflowExecutionProfileVersion,
		Capabilities: PlatformWorkflowCapabilities(true, true),
		Components: WorkflowExecutionComponents{
			CoreInterpreterID: "typed-dag-interpreter/v1", InputBuilderID: "typed-input-envelope/v1",
			ResultValidatorID: "typed-result-validator/v1", ResultApplyID: "cas-result-apply/v1",
			ReconcileID: "typed-dag-reconcile/v2", RunnerDispatchID: "builtin-runner-dispatch/v1",
			ManifestCompilerID: "application-manifest-dispatch/v1", ConditionAnalysisID: "condition-path-analysis/v2",
			ProposalAnalysisID: "proposal-lineage-analysis/v2",
		},
	}
}

// WorkflowExecutionProfileV1Descriptor is the literal immutable descriptor
// used by already-persisted workflow-engine/v1 definitions and runs. Never
// derive it from PlatformWorkflowCapabilities: the current authoring registry
// is allowed to grow without reinterpreting historical profile identities.
func WorkflowExecutionProfileV1Descriptor() WorkflowExecutionProfileDescriptor {
	return WorkflowExecutionProfileDescriptor{
		Version:      WorkflowExecutionProfileV1Version,
		Capabilities: workflowCapabilitiesV1Snapshot(),
		Components: WorkflowExecutionComponents{
			CoreInterpreterID: "typed-dag-interpreter/v1", InputBuilderID: "typed-input-envelope/v1",
			ResultValidatorID: "typed-result-validator/v1", ResultApplyID: "cas-result-apply/v1",
			ReconcileID: "typed-dag-reconcile/v1", RunnerDispatchID: "builtin-runner-dispatch/v1",
			ManifestCompilerID: "application-manifest-dispatch/v1", ConditionAnalysisID: "condition-path-analysis/v2",
			ProposalAnalysisID: "proposal-lineage-analysis/v2",
		},
	}
}

func LegacyWorkflowExecutionProfileDescriptor() WorkflowExecutionProfileDescriptor {
	return WorkflowExecutionProfileDescriptor{
		Version:      LegacyWorkflowExecutionProfileVersion,
		Capabilities: legacyWorkflowCapabilitiesSnapshot(),
		Components: WorkflowExecutionComponents{
			CoreInterpreterID: "typed-dag-interpreter/pre-pin", InputBuilderID: "typed-input-envelope/pre-pin",
			ResultValidatorID: "typed-result-validator/pre-pin", ResultApplyID: "cas-result-apply/pre-pin",
			ReconcileID: "typed-dag-reconcile/pre-pin", RunnerDispatchID: "builtin-runner-dispatch/pre-pin",
			ManifestCompilerID: "application-manifest-dispatch/pre-pin", ConditionAnalysisID: "condition-path-analysis/pre-pin",
			ProposalAnalysisID: "proposal-lineage-analysis/pre-pin",
		},
	}
}

// legacyWorkflowCapabilitiesSnapshot is intentionally literal. Never replace
// it with PlatformWorkflowCapabilities: doing so would mutate the identity of
// every migrated pre-pin definition when the current authoring registry grows.
func legacyWorkflowCapabilitiesSnapshot() WorkflowCapabilities {
	return WorkflowCapabilities{
		Version: 3,
		NodeTypes: []domain.WorkflowNodeType{
			domain.NodeArtifactInput, domain.NodeAITransform, domain.NodeCondition, domain.NodeFanOut,
			domain.NodeHumanEdit, domain.NodeManifestCompiler, domain.NodeMerge, domain.NodePublish,
			domain.NodeQualityGate, domain.NodeReviewGate, domain.NodeTransform, domain.NodeWorkbenchBuild,
		},
		InputContracts: []domain.WorkflowInputContract{
			{Capability: domain.WorkflowInputProjectBrief, ManifestJobTypes: []string{"conversation.workflow_intent", "workflow_start"}, ArtifactKinds: []string{"project_brief"}, MinimumArtifacts: 1, MaximumArtifacts: 1, RequiredSourcePurposes: []string{"project_brief"}, ManifestSchemaContracts: map[string]string{"conversation.workflow_intent": "workflow-intent-input/v1", "workflow_start": "workflow-input/v1"}},
			{Capability: domain.WorkflowInputBlueprintSelection, ManifestJobTypes: []string{"blueprint.selection"}, ArtifactKinds: []string{"blueprint"}, MinimumArtifacts: 2, MaximumArtifacts: 101, RequireApproved: true, RequiredSourcePurposes: []string{"blueprint_selection_node", "blueprint_selection_root"}, ManifestSchemaContracts: map[string]string{"blueprint.selection": "blueprint-selection/v1"}},
		},
		OutputContracts: []domain.WorkflowOutputContract{{Capability: domain.WorkflowOutputApplication, ProducedArtifactKinds: []string{"workspace"}, TerminalOutcome: domain.WorkflowOutcomeDeployment, TerminalNodeType: domain.NodePublish}},
		AITransforms: []AITransformCapability{
			{JobType: "refine_project_brief", OutputSchemaVersion: "project-brief-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"project_brief"}, ProducedArtifactKinds: []string{"project_brief"}},
			{JobType: "derive_requirements", OutputSchemaVersion: "requirements-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"project_brief"}, RequiredApprovedKinds: []string{"project_brief"}, ProducedArtifactKinds: []string{"product_requirements"}},
			{JobType: "decompose_pages", OutputSchemaVersion: "blueprint-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"product_requirements"}, RequiredApprovedKinds: []string{"product_requirements"}, ProducedArtifactKinds: []string{"blueprint"}},
			{JobType: "generate_page_spec", OutputSchemaVersion: "page-spec-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"blueprint"}, RequiredApprovedKinds: []string{"blueprint"}, ProducedArtifactKinds: []string{"page_spec"}},
			{JobType: "generate_prototype", OutputSchemaVersion: "prototype-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"page_spec"}, RequiredApprovedKinds: []string{"page_spec"}, ProducedArtifactKinds: []string{"prototype"}},
		},
		ManifestCompilers: []ManifestCompilerCapability{{
			ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1",
			RequiredArtifactKinds: []string{"blueprint", "page_spec", "prototype"}, RequiredApprovedKinds: []string{"blueprint", "page_spec", "prototype"}, RequiresMergedSlices: true,
			ProducedSemanticKinds: []string{"application_build_manifest"}, AllowedContextArtifactKinds: []string{"project_brief", "product_requirements", "decision_record", "glossary_policy", "reference_source", "change_request", "requirement_baseline", "blueprint", "page_spec", "prototype", "prototype_flow", "fixture_bundle", "api_contract", "data_contract", "permission_contract", "design_system", "token_set", "component_registry"},
		}},
		Transforms: []string{"selection_passthrough"}, FanOutItemKinds: []string{"blueprint_page", "blueprint_selection_page"},
		FanOutMaximumItems: map[string]int{"blueprint_page": domain.MaximumWorkflowFanOutItems, "blueprint_selection_page": domain.MaximumWorkflowFanOutItems},
		QualityGates:       []string{"release"}, PublishEnvironments: []string{"preview", "production"}, WorkbenchSchemaVersions: []int{1},
		AnalysisLimits: WorkflowAnalysisLimits{MaximumDefinitionNodes: 200, MaximumDefinitionEdges: 1000, MaxSemanticPathStates: 256, MaximumConditionExpression: 8 << 10},
	}
}

// workflowCapabilitiesV1Snapshot is intentionally literal. It is the exact
// capability payload hashed into workflow-engine/v1 and must not reference
// current capability factories or contract helpers.
func workflowCapabilitiesV1Snapshot() WorkflowCapabilities {
	return WorkflowCapabilities{
		Version: 4,
		NodeTypes: []domain.WorkflowNodeType{
			domain.NodeAITransform, domain.NodeArtifactInput, domain.NodeCondition, domain.NodeFanOut,
			domain.NodeHumanEdit, domain.NodeManifestCompiler, domain.NodeMerge, domain.NodePublish,
			domain.NodeQualityGate, domain.NodeReviewGate, domain.NodeTransform, domain.NodeWorkbenchBuild,
		},
		InputContracts: []domain.WorkflowInputContract{
			{Capability: domain.WorkflowInputProjectBrief, ManifestJobTypes: []string{"conversation.workflow_intent", "workflow_start"}, ArtifactKinds: []string{"project_brief"}, MinimumArtifacts: 1, MaximumArtifacts: 1, RequiredSourcePurposes: []string{"project_brief"}, ManifestSchemaContracts: map[string]string{"conversation.workflow_intent": "workflow-intent-input/v1", "workflow_start": "workflow-input/v1"}},
			{Capability: domain.WorkflowInputBlueprintSelection, ManifestJobTypes: []string{"blueprint.selection"}, ArtifactKinds: []string{"blueprint"}, MinimumArtifacts: 2, MaximumArtifacts: 101, RequireApproved: true, RequiredSourcePurposes: []string{"blueprint_selection_node", "blueprint_selection_root"}, ManifestSchemaContracts: map[string]string{"blueprint.selection": "blueprint-selection/v1"}},
		},
		OutputContracts: []domain.WorkflowOutputContract{{Capability: domain.WorkflowOutputApplication, ProducedArtifactKinds: []string{"workspace"}, TerminalOutcome: domain.WorkflowOutcomeDeployment, TerminalNodeType: domain.NodePublish}},
		AITransforms: []AITransformCapability{
			{JobType: "refine_project_brief", OutputSchemaVersion: "project-brief-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"project_brief"}, ProducedArtifactKinds: []string{"project_brief"}},
			{JobType: "derive_requirements", OutputSchemaVersion: "requirements-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"project_brief"}, RequiredApprovedKinds: []string{"project_brief"}, ProducedArtifactKinds: []string{"product_requirements"}},
			{JobType: "decompose_pages", OutputSchemaVersion: "blueprint-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"product_requirements"}, RequiredApprovedKinds: []string{"product_requirements"}, ProducedArtifactKinds: []string{"blueprint"}},
			{JobType: "generate_page_spec", OutputSchemaVersion: "page-spec-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"blueprint"}, RequiredApprovedKinds: []string{"blueprint"}, ProducedArtifactKinds: []string{"page_spec"}},
			{JobType: "generate_prototype", OutputSchemaVersion: "prototype-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"page_spec"}, RequiredApprovedKinds: []string{"page_spec"}, ProducedArtifactKinds: []string{"prototype"}},
		},
		ManifestCompilers: []ManifestCompilerCapability{{
			ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1",
			RequiredArtifactKinds: []string{"blueprint", "page_spec", "prototype"}, RequiredApprovedKinds: []string{"blueprint", "page_spec", "prototype"}, RequiresMergedSlices: true,
			ProducedSemanticKinds: []string{"application_build_manifest"}, AllowedContextArtifactKinds: []string{"project_brief", "product_requirements", "decision_record", "glossary_policy", "reference_source", "change_request", "requirement_baseline", "blueprint", "page_spec", "prototype", "prototype_flow", "fixture_bundle", "api_contract", "data_contract", "permission_contract", "design_system", "token_set", "component_registry"},
		}},
		Transforms: []string{"selection_passthrough"}, FanOutItemKinds: []string{"blueprint_page", "blueprint_selection_page"},
		FanOutMaximumItems: map[string]int{"blueprint_page": domain.MaximumWorkflowFanOutItems, "blueprint_selection_page": domain.MaximumWorkflowFanOutItems},
		QualityGates:       []string{"release"}, PublishEnvironments: []string{"preview", "production"}, WorkbenchSchemaVersions: []int{1},
		AnalysisLimits: WorkflowAnalysisLimits{MaximumDefinitionNodes: 200, MaximumDefinitionEdges: 1000, MaxSemanticPathStates: 256, MaximumConditionExpression: 8 << 10},
	}
}

func CurrentWorkflowExecutionProfileRef() domain.WorkflowExecutionProfileRef {
	ref, err := CurrentWorkflowExecutionProfileDescriptor().Ref()
	if err != nil {
		panic(err)
	}
	expected := domain.WorkflowExecutionProfileRef{Version: CurrentWorkflowExecutionProfileVersion, Hash: CurrentWorkflowExecutionProfileHash}
	if ref != expected {
		panic("current workflow execution profile descriptor changed without a version/hash bump")
	}
	return expected
}

func WorkflowExecutionProfileV1Ref() domain.WorkflowExecutionProfileRef {
	ref, err := WorkflowExecutionProfileV1Descriptor().Ref()
	if err != nil {
		panic(err)
	}
	expected := domain.WorkflowExecutionProfileRef{Version: WorkflowExecutionProfileV1Version, Hash: WorkflowExecutionProfileV1Hash}
	if ref != expected {
		panic("workflow-engine/v1 execution profile descriptor drifted from its frozen snapshot")
	}
	return expected
}

func LegacyWorkflowExecutionProfileRef() domain.WorkflowExecutionProfileRef {
	ref, err := LegacyWorkflowExecutionProfileDescriptor().Ref()
	if err != nil {
		panic(err)
	}
	expected := domain.WorkflowExecutionProfileRef{Version: LegacyWorkflowExecutionProfileVersion, Hash: LegacyWorkflowExecutionProfileHash}
	if ref != expected {
		panic("legacy workflow execution profile descriptor is not the frozen migration snapshot")
	}
	return expected
}

func normalizeDefinitionRecordProfile(record *DefinitionRecord) error {
	if record == nil {
		return fmt.Errorf("workflow definition record is required")
	}
	embedded := record.Definition.ExecutionProfile
	if embedded.IsZero() {
		legacy := LegacyWorkflowExecutionProfileRef()
		if record.ExecutionProfile.IsZero() {
			record.ExecutionProfile = legacy
		}
		if record.ExecutionProfile != legacy {
			return fmt.Errorf("pre-pin workflow definition content must use the legacy execution profile")
		}
		return nil
	}
	if err := embedded.Validate(); err != nil {
		return err
	}
	if record.ExecutionProfile.IsZero() {
		record.ExecutionProfile = embedded
	}
	if record.ExecutionProfile != embedded {
		return fmt.Errorf("workflow definition content and row execution profiles differ")
	}
	return nil
}

type profileBuildInputsFunc func(*RunRecord, domain.WorkflowDefinition, *NodeRecord) (domain.NodeInputEnvelope, error)
type profileExecuteNodeFunc func(context.Context, *Engine, workflowExecutionRuntime, Execution) (WorkerResult, error)
type profileValidateResultFunc func(context.Context, *Engine, workflowExecutionRuntime, *RunRecord, domain.WorkflowDefinition, *NodeRecord, Execution, *WorkerResult) error
type profileApplyResultFunc func(context.Context, *Engine, workflowExecutionRuntime, *RunRecord, domain.WorkflowDefinition, *NodeRecord, Lease, Execution, WorkerResult) error
type profileReconcileFunc func(*Engine, workflowExecutionRuntime, *RunRecord, domain.WorkflowDefinition, *mutationBuilder)
type profileRuntimeFactory func(workflowExecutionRuntime, WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error)

// workflowExecutionRuntime is captured exactly once and then installed in every
// registered profile bundle. Interface values are intentionally copied out of
// the mutable bootstrap registries: replacing runner/compiler/condition,
// start-input, or human control-plane validators after a run starts can
// therefore never reinterpret that run.
type workflowExecutionRuntime struct {
	runners             map[domain.WorkflowNodeType]WorkerRunner
	manifestCompilers   map[string]BuildManifestHook
	conditionEvaluator  ConditionEvaluator
	manifestFreezer     ManifestFreezer
	artifactInputs      ArtifactInputValidator
	startArtifactKinds  StartArtifactKindResolver
	proposalDispatcher  ProposalDispatcher
	buildManifestHook   BuildManifestHook
	humanEditOutput     HumanEditOutputValidator
	workbenchCompletion WorkbenchCompletionValidator
	reviewGate          ReviewGateVerifier
	humanContextKeys    map[string]struct{}
}

type workflowNodeDispatchOwner string

const (
	workflowNodeDispatchInterpreter workflowNodeDispatchOwner = "interpreter"
	workflowNodeDispatchRunner      workflowNodeDispatchOwner = "runner"
)

// frozenV0V1NodeDispatchOwnership makes the dispatch boundary explicit. Nodes
// handled by executeNodeV0V1Frozen belong to the interpreter; every other
// capability below must have an exact runner captured in the sealed profile.
// A future profile must provide its own complete ownership map, so adding a
// capability cannot silently fall through to ErrRunnerNotFound at execution.
func frozenV0V1NodeDispatchOwnership(descriptor WorkflowExecutionProfileDescriptor) map[domain.WorkflowNodeType]workflowNodeDispatchOwner {
	result := make(map[domain.WorkflowNodeType]workflowNodeDispatchOwner, len(descriptor.Capabilities.NodeTypes))
	for _, nodeType := range descriptor.Capabilities.NodeTypes {
		switch nodeType {
		case domain.NodeArtifactInput, domain.NodeAITransform, domain.NodeAI,
			domain.NodeHumanEdit, domain.NodeHumanTask, domain.NodeReviewGate,
			domain.NodeApproval, domain.NodeCondition, domain.NodeMerge,
			domain.NodeManifestCompiler:
			result[nodeType] = workflowNodeDispatchInterpreter
		case domain.NodeFanOut, domain.NodeQualityGate, domain.NodeWorkbenchBuild,
			domain.NodePublish, domain.NodeTransform, domain.NodeDelivery:
			result[nodeType] = workflowNodeDispatchRunner
		}
	}
	return result
}

func (e *Engine) captureExecutionRuntime() workflowExecutionRuntime {
	runtime := workflowExecutionRuntime{
		runners:             map[domain.WorkflowNodeType]WorkerRunner{},
		manifestCompilers:   map[string]BuildManifestHook{},
		conditionEvaluator:  e.ConditionEvaluator,
		manifestFreezer:     e.ManifestFreezer,
		artifactInputs:      e.ArtifactInputs,
		startArtifactKinds:  e.StartArtifactKinds,
		proposalDispatcher:  e.ProposalDispatcher,
		buildManifestHook:   e.BuildManifestHook,
		humanEditOutput:     e.HumanEditOutput,
		workbenchCompletion: e.WorkbenchCompletion,
		reviewGate:          e.ReviewGate,
		humanContextKeys:    map[string]struct{}{},
	}
	for key := range e.HumanWorkflowContextKeys {
		runtime.humanContextKeys[key] = struct{}{}
	}
	if e.Runners != nil {
		for _, nodeType := range e.ExecutionProfiles.registeredNodeTypes() {
			if runner, exists := e.Runners.RunnerFor(nodeType); exists {
				runtime.runners[nodeType] = runner
			}
		}
	}
	if e.ManifestCompilers != nil {
		runtime.manifestCompilers = e.ManifestCompilers.snapshot()
	}
	return runtime
}

func cloneExecutionRuntime(runtime workflowExecutionRuntime) workflowExecutionRuntime {
	clone := runtime
	clone.runners = make(map[domain.WorkflowNodeType]WorkerRunner, len(runtime.runners))
	for nodeType, runner := range runtime.runners {
		clone.runners[nodeType] = runner
	}
	clone.manifestCompilers = make(map[string]BuildManifestHook, len(runtime.manifestCompilers))
	for key, compiler := range runtime.manifestCompilers {
		clone.manifestCompilers[key] = compiler
	}
	clone.humanContextKeys = make(map[string]struct{}, len(runtime.humanContextKeys))
	for key := range runtime.humanContextKeys {
		clone.humanContextKeys[key] = struct{}{}
	}
	return clone
}

// WorkflowExecutionProfileBundle is the executable counterpart of a hashed
// descriptor. Function fields are deliberately private: callers select only by
// exact ref and cannot swap a component beneath an immutable profile identity.
type WorkflowExecutionProfileBundle struct {
	Descriptor        WorkflowExecutionProfileDescriptor
	componentIdentity WorkflowExecutionComponents
	nodeDispatch      map[domain.WorkflowNodeType]workflowNodeDispatchOwner
	runtime           *workflowExecutionRuntime
	runtimeFactory    profileRuntimeFactory
	buildInputs       profileBuildInputsFunc
	executeNodeFn     profileExecuteNodeFunc
	validateResultFn  profileValidateResultFunc
	applyResultFn     profileApplyResultFunc
	reconcileFn       profileReconcileFunc
}

func (b WorkflowExecutionProfileBundle) executionRuntime(engine *Engine) workflowExecutionRuntime {
	if b.runtime != nil {
		return cloneExecutionRuntime(*b.runtime)
	}
	// An unregistered bundle is used only by focused unit tests. Production and
	// worker paths resolve sealed bundles from WorkflowExecutionProfileRegistry.
	if engine == nil {
		return workflowExecutionRuntime{}
	}
	return engine.captureExecutionRuntime()
}

func (b WorkflowExecutionProfileBundle) executeNode(ctx context.Context, engine *Engine, execution Execution) (WorkerResult, error) {
	return b.executeNodeFn(ctx, engine, b.executionRuntime(engine), execution)
}

func (b WorkflowExecutionProfileBundle) validateResult(ctx context.Context, engine *Engine, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, execution Execution, result *WorkerResult) error {
	return b.validateResultFn(ctx, engine, b.executionRuntime(engine), run, definition, node, execution, result)
}

func (b WorkflowExecutionProfileBundle) applyResult(ctx context.Context, engine *Engine, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, lease Lease, execution Execution, result WorkerResult) error {
	return b.applyResultFn(ctx, engine, b.executionRuntime(engine), run, definition, node, lease, execution, result)
}

func (b WorkflowExecutionProfileBundle) reconcile(engine *Engine, run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	b.reconcileFn(engine, b.executionRuntime(engine), run, definition, builder)
}

type WorkflowExecutionProfileRegistry struct {
	mu      sync.RWMutex
	bundles map[domain.WorkflowExecutionProfileRef]WorkflowExecutionProfileBundle
	sealed  bool
}

func NewWorkflowExecutionProfileRegistry() *WorkflowExecutionProfileRegistry {
	return &WorkflowExecutionProfileRegistry{bundles: map[domain.WorkflowExecutionProfileRef]WorkflowExecutionProfileBundle{}}
}

func NewBuiltinWorkflowExecutionProfileRegistry() (*WorkflowExecutionProfileRegistry, error) {
	registry := NewWorkflowExecutionProfileRegistry()
	// Every persisted profile keeps an independent registration. Historical refs
	// must never be redirected to the current interpreter behavior.
	for _, bundle := range []WorkflowExecutionProfileBundle{
		legacyExecutionProfileBundle(), workflowExecutionProfileV1Bundle(), currentExecutionProfileBundle(),
	} {
		if err := registry.Register(bundle); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func legacyExecutionProfileBundle() WorkflowExecutionProfileBundle {
	descriptor := LegacyWorkflowExecutionProfileDescriptor()
	return WorkflowExecutionProfileBundle{
		Descriptor: descriptor, componentIdentity: descriptor.Components,
		nodeDispatch:   frozenV0V1NodeDispatchOwnership(descriptor),
		runtimeFactory: sealRuntimeV0,
		buildInputs:    buildNodeInputEnvelopeV0, executeNodeFn: executeNodeV0,
		validateResultFn: validateResultV0, applyResultFn: applyResultV0, reconcileFn: reconcileV0,
	}
}

func currentExecutionProfileBundle() WorkflowExecutionProfileBundle {
	descriptor := CurrentWorkflowExecutionProfileDescriptor()
	return WorkflowExecutionProfileBundle{
		Descriptor: descriptor, componentIdentity: descriptor.Components,
		nodeDispatch:   frozenV0V1NodeDispatchOwnership(descriptor),
		runtimeFactory: sealRuntimeV2,
		buildInputs:    buildNodeInputEnvelopeV2, executeNodeFn: executeNodeV2,
		validateResultFn: validateResultV2, applyResultFn: applyResultV2, reconcileFn: reconcileV2,
	}
}

func workflowExecutionProfileV1Bundle() WorkflowExecutionProfileBundle {
	descriptor := WorkflowExecutionProfileV1Descriptor()
	return WorkflowExecutionProfileBundle{
		Descriptor: descriptor, componentIdentity: descriptor.Components,
		nodeDispatch:   frozenV0V1NodeDispatchOwnership(descriptor),
		runtimeFactory: sealRuntimeV1,
		buildInputs:    buildNodeInputEnvelopeV1, executeNodeFn: executeNodeV1,
		validateResultFn: validateResultV1, applyResultFn: applyResultV1, reconcileFn: reconcileV1,
	}
}

func (r *WorkflowExecutionProfileRegistry) Register(bundle WorkflowExecutionProfileBundle) error {
	if r == nil || bundle.runtime != nil || bundle.runtimeFactory == nil || bundle.buildInputs == nil || bundle.executeNodeFn == nil || bundle.validateResultFn == nil || bundle.applyResultFn == nil || bundle.reconcileFn == nil {
		return fmt.Errorf("complete workflow execution profile bundle is required")
	}
	if bundle.componentIdentity != bundle.Descriptor.Components {
		return fmt.Errorf("workflow execution profile component identity does not match its descriptor")
	}
	if err := validateNodeDispatchOwnership(bundle.Descriptor, bundle.nodeDispatch); err != nil {
		return err
	}
	ref, err := bundle.Descriptor.Ref()
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("workflow execution profile registry is sealed")
	}
	if _, exists := r.bundles[ref]; exists {
		return fmt.Errorf("workflow execution profile %s/%s is already registered", ref.Version, ref.Hash)
	}
	bundle.Descriptor, err = cloneWorkflowExecutionProfileDescriptor(bundle.Descriptor)
	if err != nil {
		return err
	}
	bundle.nodeDispatch = cloneNodeDispatchOwnership(bundle.nodeDispatch)
	r.bundles[ref] = bundle
	return nil
}

// Seal binds every immutable descriptor to the one bootstrap-time runtime
// snapshot. Once sealed, neither bundles nor their dispatch tables can be
// replaced under an existing descriptor hash.
func (r *WorkflowExecutionProfileRegistry) Seal(runtime workflowExecutionRuntime) error {
	if r == nil {
		return fmt.Errorf("workflow execution profile registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return nil
	}
	sealedBundles := make(map[domain.WorkflowExecutionProfileRef]WorkflowExecutionProfileBundle, len(r.bundles))
	for ref, bundle := range r.bundles {
		computed, err := bundle.Descriptor.Ref()
		if err != nil || computed != ref || bundle.componentIdentity != bundle.Descriptor.Components {
			return fmt.Errorf("workflow execution profile bundle identity mismatch")
		}
		copyRuntime, err := bundle.runtimeFactory(cloneExecutionRuntime(runtime), bundle.Descriptor)
		if err != nil {
			return fmt.Errorf("seal workflow execution profile %s/%s: %w", ref.Version, ref.Hash, err)
		}
		if err := validateExecutionProfileRuntime(bundle, copyRuntime); err != nil {
			return fmt.Errorf("seal workflow execution profile %s/%s: %w", ref.Version, ref.Hash, err)
		}
		bundle.runtime = &copyRuntime
		sealedBundles[ref] = bundle
	}
	for ref, bundle := range sealedBundles {
		r.bundles[ref] = bundle
	}
	r.sealed = true
	return nil
}

// SealProfile binds a version-specific runtime before the registry is finally
// sealed. This is the extension seam for future profiles whose dispatch differs
// from v0/v1: each exact ref and component identity is verified independently.
func (r *WorkflowExecutionProfileRegistry) SealProfile(ref domain.WorkflowExecutionProfileRef, components WorkflowExecutionComponents, runtime workflowExecutionRuntime) error {
	if r == nil || ref.Validate() != nil {
		return fmt.Errorf("valid workflow execution profile registry and ref are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("workflow execution profile registry is sealed")
	}
	bundle, exists := r.bundles[ref]
	if !exists {
		return fmt.Errorf("workflow execution profile %s/%s is not registered", ref.Version, ref.Hash)
	}
	if bundle.componentIdentity != components || bundle.Descriptor.Components != components {
		return fmt.Errorf("workflow execution profile component identity mismatch")
	}
	copyRuntime := cloneExecutionRuntime(runtime)
	if err := validateExecutionProfileRuntime(bundle, copyRuntime); err != nil {
		return fmt.Errorf("seal workflow execution profile %s/%s: %w", ref.Version, ref.Hash, err)
	}
	bundle.runtime = &copyRuntime
	r.bundles[ref] = bundle
	return nil
}

// FinishSeal makes all profile bindings immutable and fails closed if any
// registered descriptor has no exact runtime bundle.
func (r *WorkflowExecutionProfileRegistry) FinishSeal() error {
	if r == nil {
		return fmt.Errorf("workflow execution profile registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return nil
	}
	for ref, bundle := range r.bundles {
		if bundle.runtime == nil {
			return fmt.Errorf("workflow execution profile %s/%s has no runtime binding", ref.Version, ref.Hash)
		}
	}
	r.sealed = true
	return nil
}

func (r *WorkflowExecutionProfileRegistry) IsSealed() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sealed
}

func (r *WorkflowExecutionProfileRegistry) Resolve(ref domain.WorkflowExecutionProfileRef) (WorkflowExecutionProfileBundle, error) {
	if r == nil || ref.Validate() != nil {
		return WorkflowExecutionProfileBundle{}, fmt.Errorf("workflow execution profile ref is invalid")
	}
	r.mu.RLock()
	bundle, exists := r.bundles[ref]
	sealed := r.sealed
	r.mu.RUnlock()
	if !exists {
		return WorkflowExecutionProfileBundle{}, fmt.Errorf("workflow execution profile %s/%s is not registered", ref.Version, ref.Hash)
	}
	if !sealed || bundle.runtime == nil {
		return WorkflowExecutionProfileBundle{}, fmt.Errorf("workflow execution profile %s/%s has no sealed runtime bundle", ref.Version, ref.Hash)
	}
	cloned, err := cloneWorkflowExecutionProfileDescriptor(bundle.Descriptor)
	if err != nil {
		return WorkflowExecutionProfileBundle{}, err
	}
	bundle.Descriptor = cloned
	bundle.nodeDispatch = cloneNodeDispatchOwnership(bundle.nodeDispatch)
	return bundle, nil
}

func (r *WorkflowExecutionProfileRegistry) registeredNodeTypes() []domain.WorkflowNodeType {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	seen := make(map[domain.WorkflowNodeType]bool)
	for _, bundle := range r.bundles {
		for _, nodeType := range bundle.Descriptor.Capabilities.NodeTypes {
			seen[nodeType] = true
		}
	}
	r.mu.RUnlock()
	result := make([]domain.WorkflowNodeType, 0, len(seen))
	for nodeType := range seen {
		result = append(result, nodeType)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func validateNodeDispatchOwnership(
	descriptor WorkflowExecutionProfileDescriptor,
	ownership map[domain.WorkflowNodeType]workflowNodeDispatchOwner,
) error {
	declared := make(map[domain.WorkflowNodeType]bool, len(descriptor.Capabilities.NodeTypes))
	for _, nodeType := range descriptor.Capabilities.NodeTypes {
		if declared[nodeType] {
			return fmt.Errorf("workflow execution profile declares duplicate node type %s", nodeType)
		}
		declared[nodeType] = true
		owner, exists := ownership[nodeType]
		if !exists || owner != workflowNodeDispatchInterpreter && owner != workflowNodeDispatchRunner {
			return fmt.Errorf("workflow execution profile node type %s has no explicit dispatch owner", nodeType)
		}
	}
	for nodeType := range ownership {
		if !declared[nodeType] {
			return fmt.Errorf("workflow execution profile dispatch ownership includes undeclared node type %s", nodeType)
		}
	}
	runnerDispatchID := descriptor.Components.RunnerDispatchID
	if runnerDispatchID == LegacyWorkflowExecutionProfileDescriptor().Components.RunnerDispatchID ||
		runnerDispatchID == WorkflowExecutionProfileV1Descriptor().Components.RunnerDispatchID ||
		runnerDispatchID == CurrentWorkflowExecutionProfileDescriptor().Components.RunnerDispatchID {
		expected := frozenV0V1NodeDispatchOwnership(descriptor)
		if len(expected) != len(ownership) {
			return fmt.Errorf("workflow execution profile changed its frozen dispatch ownership set")
		}
		for nodeType, expectedOwner := range expected {
			if ownership[nodeType] != expectedOwner {
				return fmt.Errorf("workflow execution profile node type %s changed its frozen dispatch owner", nodeType)
			}
		}
	}
	return nil
}

func validateExecutionProfileRuntime(bundle WorkflowExecutionProfileBundle, runtime workflowExecutionRuntime) error {
	if err := validateNodeDispatchOwnership(bundle.Descriptor, bundle.nodeDispatch); err != nil {
		return err
	}
	for _, nodeType := range bundle.Descriptor.Capabilities.NodeTypes {
		if bundle.nodeDispatch[nodeType] == workflowNodeDispatchRunner && runtime.runners[nodeType] == nil {
			return fmt.Errorf("runner binding for declared node type %s is missing", nodeType)
		}
	}
	if containsNodeType(bundle.Descriptor.Capabilities.NodeTypes, domain.NodeCondition) && runtime.conditionEvaluator == nil {
		return fmt.Errorf("condition evaluator binding is missing")
	}
	for _, capability := range bundle.Descriptor.Capabilities.ManifestCompilers {
		key := manifestCompilerKey(capability.ManifestKind, capability.SchemaVersion, capability.Hook)
		if runtime.manifestCompilers[key] == nil {
			return fmt.Errorf(
				"manifest compiler binding %s/%d/%s is missing",
				capability.ManifestKind, capability.SchemaVersion, capability.Hook,
			)
		}
	}
	return nil
}

func cloneNodeDispatchOwnership(
	value map[domain.WorkflowNodeType]workflowNodeDispatchOwner,
) map[domain.WorkflowNodeType]workflowNodeDispatchOwner {
	clone := make(map[domain.WorkflowNodeType]workflowNodeDispatchOwner, len(value))
	for nodeType, owner := range value {
		clone[nodeType] = owner
	}
	return clone
}

func cloneWorkflowExecutionProfileDescriptor(value WorkflowExecutionProfileDescriptor) (WorkflowExecutionProfileDescriptor, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return WorkflowExecutionProfileDescriptor{}, err
	}
	var clone WorkflowExecutionProfileDescriptor
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return WorkflowExecutionProfileDescriptor{}, err
	}
	return clone, nil
}

func (r *WorkflowExecutionProfileRegistry) SupportedRefs() []domain.WorkflowExecutionProfileRef {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	refs := make([]domain.WorkflowExecutionProfileRef, 0, len(r.bundles))
	for ref, bundle := range r.bundles {
		if bundle.runtime != nil || !r.sealed {
			refs = append(refs, ref)
		}
	}
	r.mu.RUnlock()
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Version == refs[j].Version {
			return refs[i].Hash < refs[j].Hash
		}
		return refs[i].Version < refs[j].Version
	})
	return refs
}
