package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type Engine struct {
	Store              Store
	Runners            RunnerRegistry
	IDs                IDGenerator
	Clock              Clock
	LeaseDuration      time.Duration
	RetryBackoff       func(int) time.Duration
	ManifestFreezer    ManifestFreezer
	ArtifactInputs     ArtifactInputValidator
	StartArtifactKinds StartArtifactKindResolver
	HumanEditOutput    HumanEditOutputValidator
	// HumanWorkflowContextKeys is the explicit server-side extension allowlist.
	// Schema-declared keys are accepted without being repeated here; reserved
	// control-plane keys remain denied even if a client schema declares them.
	HumanWorkflowContextKeys map[string]struct{}
	WorkbenchCompletion      WorkbenchCompletionValidator
	ReviewGate               ReviewGateVerifier
	ProposalDispatcher       ProposalDispatcher
	BuildManifestHook        BuildManifestHook
	ManifestCompilers        *BuildManifestRegistry
	ConditionEvaluator       ConditionEvaluator
	Capabilities             WorkflowCapabilities
	ExecutionProfiles        *WorkflowExecutionProfileRegistry
	// RequireGovernedStarts is enabled by the production platform bootstrap.
	// Generic engines retain legacy start support for replay/unit fixtures.
	RequireGovernedStarts bool
}

func NewEngine(store Store) (*Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("workflow store is required")
	}
	profiles, err := NewBuiltinWorkflowExecutionProfileRegistry()
	if err != nil {
		return nil, err
	}
	return &Engine{
		Store: store, IDs: UUIDGenerator{}, Clock: realClock{}, LeaseDuration: 2 * time.Minute,
		RetryBackoff: defaultRetryBackoff, Capabilities: PlatformWorkflowCapabilities(true, true), ExecutionProfiles: profiles,
	}, nil
}

func (e *Engine) executionProfile(ref domain.WorkflowExecutionProfileRef) (WorkflowExecutionProfileBundle, error) {
	if e == nil || e.ExecutionProfiles == nil {
		return WorkflowExecutionProfileBundle{}, fmt.Errorf("workflow execution profile registry is required")
	}
	if err := e.SealExecutionProfiles(); err != nil {
		return WorkflowExecutionProfileBundle{}, err
	}
	return e.ExecutionProfiles.Resolve(ref)
}

func (e *Engine) supportedExecutionProfiles() []domain.WorkflowExecutionProfileRef {
	if e == nil || e.ExecutionProfiles == nil || !e.ExecutionProfiles.IsSealed() {
		return nil
	}
	return e.ExecutionProfiles.SupportedRefs()
}

// SealExecutionProfiles captures bootstrap dispatch exactly once. It is safe to
// call repeatedly; the first call wins and later Engine registry replacement
// cannot affect an existing profile bundle.
func (e *Engine) SealExecutionProfiles() error {
	if e == nil || e.ExecutionProfiles == nil {
		return fmt.Errorf("workflow execution profile registry is required")
	}
	if e.ExecutionProfiles.IsSealed() {
		return nil
	}
	return e.ExecutionProfiles.Seal(e.captureExecutionRuntime())
}

// SealProductionExecutionProfiles additionally proves that the runtime
// bootstrap implements the complete current v2 descriptor. Production never
// advertises a descriptor with condition/compiler/runner dispatch missing.
func (e *Engine) SealProductionExecutionProfiles() error {
	if e == nil {
		return fmt.Errorf("workflow execution engine is required")
	}
	descriptor := CurrentWorkflowExecutionProfileDescriptor()
	expectedCapabilities, err := domain.CanonicalHash(descriptor.Capabilities)
	if err != nil {
		return err
	}
	actualCapabilities, err := domain.CanonicalHash(e.Capabilities)
	if err != nil {
		return err
	}
	if expectedCapabilities != actualCapabilities {
		return fmt.Errorf("production workflow capabilities do not match the current execution profile descriptor")
	}
	runtime := e.captureExecutionRuntime()
	if runtime.conditionEvaluator == nil || runtime.manifestFreezer == nil || runtime.artifactInputs == nil || runtime.startArtifactKinds == nil || runtime.proposalDispatcher == nil || runtime.humanEditOutput == nil || runtime.workbenchCompletion == nil || runtime.reviewGate == nil {
		return fmt.Errorf("production workflow condition, manifest, artifact, proposal and human control-plane dispatch are incomplete")
	}
	if err := e.ExecutionProfiles.Seal(runtime); err != nil {
		return err
	}
	_, err = e.ExecutionProfiles.Resolve(CurrentWorkflowExecutionProfileRef())
	return err
}

// Readiness reports active nonterminal runs that this process cannot execute.
// Profile-aware claiming still permits mixed-version rolling deployments, but
// an unsupported profile with no local bundle must be observable rather than
// leaving a permanently ready node that no worker can claim.
func (e *Engine) Readiness(ctx context.Context) error {
	if e == nil || e.Store == nil || e.ExecutionProfiles == nil {
		return fmt.Errorf("workflow execution engine is not initialized")
	}
	if err := e.SealExecutionProfiles(); err != nil {
		return err
	}
	active, err := e.Store.ListActiveExecutionProfiles(ctx)
	if err != nil {
		return err
	}
	supported := make(map[domain.WorkflowExecutionProfileRef]bool)
	for _, ref := range e.supportedExecutionProfiles() {
		supported[ref] = true
	}
	for _, ref := range active {
		if !supported[ref] {
			return fmt.Errorf("active workflow run requires unsupported execution profile %s/%s", ref.Version, ref.Hash)
		}
	}
	return nil
}

type StartRequest struct {
	RunID               string
	ProjectID           string
	DefinitionVersionID string
	InputManifest       domain.ManifestRef
	Scope               json.RawMessage
	StartedBy           string
	GovernanceMode      core.GovernanceMode
	provenance          startRequestProvenance
}

type startRequestProvenance struct {
	acceptedConversationCommandID string
	definitionVersionID           string
	inputManifest                 domain.ManifestRef
	scopeHash                     string
}

// NewAcceptedConversationCommandStartRequest is the only constructor that can
// mint the private provenance required for a reviewed conversation command.
// Transport callers can still build ordinary StartRequest values, but cannot
// authorize the reserved scope.conversationIntent control-plane envelope.
func NewAcceptedConversationCommandStartRequest(
	commandID, definitionVersionID string,
	inputManifest domain.ManifestRef,
	scope json.RawMessage,
) (StartRequest, error) {
	scopeHash, err := domain.CanonicalHash(scope)
	if err != nil {
		return StartRequest{}, &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "scope", Message: "accepted conversation command scope must be canonicalizable JSON"}
	}
	return StartRequest{
		RunID: commandID, DefinitionVersionID: definitionVersionID,
		InputManifest: inputManifest, Scope: cloneRaw(scope),
		provenance: startRequestProvenance{
			acceptedConversationCommandID: commandID,
			definitionVersionID:           definitionVersionID, inputManifest: inputManifest, scopeHash: scopeHash,
		},
	}, nil
}

// AcceptedConversationCommandID exposes immutable provenance for diagnostics
// and contract tests without allowing callers to manufacture it.
func (r StartRequest) AcceptedConversationCommandID() (string, bool) {
	commandID := strings.TrimSpace(r.provenance.acceptedConversationCommandID)
	return commandID, commandID != ""
}

func validateStartRequestScope(request StartRequest) error {
	if len(request.Scope) == 0 {
		if _, trusted := request.AcceptedConversationCommandID(); trusted {
			return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "scope.conversationIntent", Message: "accepted conversation command provenance requires its reviewed intent envelope"}
		}
		return nil
	}
	var scope map[string]json.RawMessage
	if err := json.Unmarshal(request.Scope, &scope); err != nil || scope == nil {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "scope", Message: "workflow scope must be a JSON object"}
	}
	_, reserved := scope["conversationIntent"]
	commandID, trusted := request.AcceptedConversationCommandID()
	if !reserved {
		if trusted {
			return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "scope.conversationIntent", Message: "accepted conversation command provenance requires its reviewed intent envelope"}
		}
		return nil
	}
	if !trusted || commandID != strings.TrimSpace(request.RunID) {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "scope.conversationIntent", Message: "reserved conversation intent scope requires an accepted server command"}
	}
	scopeHash, err := domain.CanonicalHash(request.Scope)
	if err != nil || request.provenance.definitionVersionID != request.DefinitionVersionID ||
		request.provenance.inputManifest != request.InputManifest || request.provenance.scopeHash != scopeHash {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "scope.conversationIntent", Message: "accepted conversation command provenance does not match the exact start request"}
	}
	var intent map[string]json.RawMessage
	if err := json.Unmarshal(scope["conversationIntent"], &intent); err != nil || intent == nil {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "scope.conversationIntent", Message: "reviewed conversation intent envelope must be a JSON object"}
	}
	return nil
}

func (e *Engine) Start(ctx context.Context, request StartRequest) (*RunRecord, error) {
	if err := validateStartRequestScope(request); err != nil {
		return nil, err
	}
	definitionRecord, err := e.Store.GetDefinitionVersion(ctx, request.DefinitionVersionID)
	if err != nil {
		return nil, err
	}
	if !definitionRecord.Published {
		return nil, fmt.Errorf("workflow definition version is not published")
	}
	selectedProfile := definitionRecord.ExecutionProfile
	currentProfile := CurrentWorkflowExecutionProfileRef()
	legacyProfile := LegacyWorkflowExecutionProfileRef()
	currentDefinition := selectedProfile == currentProfile && definitionRecord.Definition.ExecutionProfile == currentProfile
	isolatedLegacyDefinition := !e.RequireGovernedStarts && selectedProfile == legacyProfile &&
		definitionRecord.Definition.ExecutionProfile == legacyProfile
	if !currentDefinition && !isolatedLegacyDefinition {
		return nil, &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "workflow.executionProfile", Message: "new production runs require the current exact execution profile"}
	}
	profile, err := e.executionProfile(selectedProfile)
	if err != nil {
		return nil, err
	}
	runtime := profile.executionRuntime(e)
	if definitionRecord.ProjectID != "" && definitionRecord.ProjectID != request.ProjectID {
		return nil, fmt.Errorf("workflow definition belongs to another project")
	}
	if err := ValidateDefinitionForExecutionProfile(definitionRecord.Definition, definitionRecord.ExecutionProfile); err != nil {
		return nil, err
	}
	if e.RequireGovernedStarts && definitionRecord.Definition.InputContract == nil {
		return nil, &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "workflow.inputContract", Message: "production workflow starts require a governed input contract"}
	}
	manifest, err := e.Store.GetManifest(ctx, request.InputManifest.ID)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if manifest.Ref() != request.InputManifest || manifest.ProjectID != request.ProjectID {
		return nil, domain.ErrManifestUnpinned
	}
	if err := ValidateStartManifestJobType(definitionRecord.Definition, manifest.JobType); err != nil {
		return nil, &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "inputManifest.jobType", Message: "the input manifest job type is incompatible with the selected workflow definition"}
	}
	metadata := StartArtifactMetadata{}
	if definitionRecord.Definition.InputContract != nil {
		if runtime.startArtifactKinds == nil {
			return nil, fmt.Errorf("workflow start artifact-kind resolver is required")
		}
		if resolver, ok := runtime.startArtifactKinds.(StartArtifactMetadataResolver); ok {
			metadata, err = resolver.ResolveStartArtifactMetadata(ctx, manifest)
			if err != nil {
				return nil, err
			}
		} else {
			metadata.Kinds, err = runtime.startArtifactKinds.ResolveStartArtifactKinds(ctx, manifest)
			if err != nil {
				return nil, err
			}
			metadata.Count = len(artifactInputRefs(manifest))
			if definitionRecord.Definition.InputContract.RequireApproved {
				return nil, fmt.Errorf("workflow start approval metadata resolver is required")
			}
		}
	}
	if err := CompatibleStart(definitionRecord.Definition, DescribeStartManifest(manifest, metadata), ""); err != nil {
		return nil, &domain.DomainError{
			Kind: domain.ErrInvalidArgument, Field: "inputManifest",
			Message: "the input manifest contract is incompatible with the selected workflow definition",
		}
	}
	entry, err := definitionRecord.Definition.EntryNodeID()
	if err != nil {
		return nil, err
	}
	runID := request.RunID
	if runID == "" {
		runID = e.id()
	}
	now := e.now()
	contextState := NewRunContext()
	templates, err := fanOutTemplateNodes(definitionRecord.Definition)
	if err != nil {
		return nil, err
	}
	governanceMode := request.GovernanceMode
	if governanceMode == "" {
		governanceMode = core.GovernanceModeTeam
	}
	run := &RunRecord{ID: runID, ProjectID: request.ProjectID, DefinitionVersionID: request.DefinitionVersionID, Definition: definitionRecord.Definition.RefForExecutionProfile(selectedProfile), ExecutionProfile: selectedProfile, InputManifest: &request.InputManifest, Status: RunRunning, GovernanceMode: governanceMode, Scope: cloneRaw(request.Scope), Context: contextState, StartedBy: request.StartedBy, StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now, Nodes: map[string]*NodeRecord{}}
	if len(run.Scope) == 0 {
		run.Scope = json.RawMessage(`{}`)
	}
	for _, nodeDefinition := range definitionRecord.Definition.Nodes {
		if templates[nodeDefinition.ID] {
			continue
		}
		status := NodePending
		if nodeDefinition.ID == entry {
			if _, _, required := nodeExecutionPolicy(nodeDefinition); required {
				status = NodeWaitingInput
				run.Status = RunWaitingInput
			} else {
				status = NodeReady
			}
		}
		attempts, timeout := nodeLimits(nodeDefinition)
		node := &NodeRecord{ID: e.id(), RunID: run.ID, Key: nodeDefinition.ID, DefinitionNodeID: nodeDefinition.ID, Type: nodeDefinition.Type, Status: status, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
		run.Nodes[node.Key] = node
		run.Context.Nodes[node.Key] = NodeMetadata{DefinitionNodeID: nodeDefinition.ID, MaxAttempts: attempts, TimeoutNanos: int64(timeout)}
	}
	if err := run.Validate(); err != nil {
		return nil, err
	}
	events := []Event{{ID: e.id(), RunID: run.ID, Type: "run.started", Payload: mustJSON(map[string]any{"definitionVersionId": request.DefinitionVersionID, "manifestId": request.InputManifest.ID, "executionProfile": selectedProfile, "governanceMode": governanceMode}), ActorID: request.StartedBy, CreatedAt: now}}
	if entryNode := run.Nodes[entry]; entryNode != nil && entryNode.Status == NodeWaitingInput {
		events = append(events, Event{ID: e.id(), RunID: run.ID, Type: "node.execution_authorization_required", NodeKey: entry, Payload: json.RawMessage(`{}`), CreatedAt: now})
	}
	if err := e.Store.CreateRun(ctx, run, events); err != nil {
		return nil, err
	}
	return e.Store.GetRun(ctx, run.ID)
}

func (e *Engine) ClaimAndExecute(ctx context.Context, workerID string) error {
	if err := e.SealExecutionProfiles(); err != nil {
		return err
	}
	lease, err := e.Store.ClaimRunnable(ctx, workerID, e.now(), e.leaseDuration(), e.supportedExecutionProfiles()...)
	if err != nil {
		return err
	}
	return e.ExecuteLease(ctx, lease)
}

func (e *Engine) ExecuteLease(ctx context.Context, lease Lease) error {
	run, definitionRecord, node, definition, err := e.loadExecution(ctx, lease.RunID, lease.NodeKey)
	if err != nil {
		return err
	}
	if node.Status != NodeRunning || node.LeaseOwner != lease.WorkerID || node.ID != lease.NodeID || node.Attempt != lease.Attempt {
		return ErrLeaseLost
	}
	profile, err := e.executionProfile(run.ExecutionProfile)
	if err != nil {
		return err
	}
	inputs, err := profile.buildInputs(run, definitionRecord.Definition, node)
	if err != nil {
		return e.handleFailure(ctx, run, definitionRecord.Definition, node, lease, err)
	}
	execution := Execution{Run: *run, Node: *node, Definition: definition, Workflow: definitionRecord.Definition, Lease: lease, Inputs: inputs}
	if _, _, required := nodeExecutionPolicy(definition); required && run.Context.Nodes[node.Key].ExecutionActor == nil {
		return profile.applyResult(ctx, e, run, definitionRecord.Definition, node, lease, execution, WorkerResult{Disposition: ResultWaitInput})
	}
	result, runErr := profile.executeNode(ctx, e, execution)
	if runErr != nil {
		return e.handleFailure(ctx, run, definitionRecord.Definition, node, lease, runErr)
	}
	return profile.applyResult(ctx, e, run, definitionRecord.Definition, node, lease, execution, result)
}

// executeNodeV0V1Frozen is the migration-cut interpreter shared by the two
// already-issued profile descriptors. Never change its semantics for a future
// engine revision; add executeNodeV2 and a new profile bundle instead.
func (e *Engine) executeNodeV0V1Frozen(ctx context.Context, runtime workflowExecutionRuntime, execution Execution) (WorkerResult, error) {
	node := execution.Definition
	switch node.Type {
	case domain.NodeArtifactInput:
		if execution.Run.InputManifest == nil {
			return WorkerResult{}, fmt.Errorf("artifact input requires a frozen manifest")
		}
		manifest, err := e.Store.GetManifest(ctx, execution.Run.InputManifest.ID)
		if err != nil {
			return WorkerResult{}, err
		}
		output := mustJSON(map[string]any{"payload": map[string]any{"manifestId": manifest.ID, "manifestHash": manifest.Hash}})
		if runtime.artifactInputs != nil {
			output, err = runtime.artifactInputs.Validate(ctx, execution, manifest)
			if err != nil {
				return WorkerResult{}, err
			}
		}
		return WorkerResult{Disposition: ResultComplete, Output: output}, nil
	case domain.NodeHumanEdit, domain.NodeHumanTask:
		return WorkerResult{Disposition: ResultWaitInput}, nil
	case domain.NodeReviewGate, domain.NodeApproval:
		return WorkerResult{Disposition: ResultWaitReview}, nil
	case domain.NodeMerge:
		return WorkerResult{Disposition: ResultComplete}, nil
	case domain.NodeAITransform, domain.NodeAI:
		if runtime.manifestFreezer != nil {
			manifest, err := runtime.manifestFreezer.Freeze(ctx, execution)
			if err != nil {
				return WorkerResult{}, err
			}
			var proposal *domain.ProposalRef
			if runtime.proposalDispatcher != nil {
				proposal, err = runtime.proposalDispatcher.Dispatch(ctx, execution, manifest)
				if err != nil {
					return WorkerResult{}, err
				}
			}
			disposition := ResultWaitInput
			if proposal != nil {
				disposition = ResultComplete
			}
			return WorkerResult{Disposition: disposition, Manifest: &manifest, Proposal: proposal}, nil
		}
	case domain.NodeManifestCompiler:
		if execution.Definition.ManifestCompiler != nil && len(runtime.manifestCompilers) > 0 {
			config := execution.Definition.ManifestCompiler
			compiler := runtime.manifestCompilers[manifestCompilerKey(config.ManifestKind, config.SchemaVersion, config.Hook)]
			if compiler == nil {
				return WorkerResult{}, fmt.Errorf("%w for manifest compiler %s/%d/%s", ErrRunnerNotFound, config.ManifestKind, config.SchemaVersion, config.Hook)
			}
			manifest, err := compiler.Compile(ctx, execution)
			if err != nil {
				return WorkerResult{}, err
			}
			if err := manifest.Freeze(); err != nil {
				return WorkerResult{}, err
			}
			return WorkerResult{Disposition: ResultComplete, BuildManifest: &manifest}, nil
		}
		// Legacy definitions and isolated engine tests retain their injected hook;
		// governed platform definitions always use the exact dispatcher above.
		if execution.Workflow.InputContract == nil && runtime.buildManifestHook != nil && execution.Definition.ManifestCompiler != nil {
			manifest, err := runtime.buildManifestHook.Compile(ctx, execution)
			if err != nil {
				return WorkerResult{}, err
			}
			if err := manifest.Freeze(); err != nil {
				return WorkerResult{}, err
			}
			return WorkerResult{Disposition: ResultComplete, BuildManifest: &manifest}, nil
		}
	case domain.NodeCondition:
		if runtime.conditionEvaluator != nil {
			branch, err := runtime.conditionEvaluator.Evaluate(ctx, execution, node.Condition.Branches)
			if err != nil {
				return WorkerResult{}, err
			}
			return WorkerResult{Disposition: ResultComplete, Branch: branch}, nil
		}
	}
	runner, exists := runtime.runners[node.Type]
	if !exists {
		return WorkerResult{}, fmt.Errorf("%w for %s", ErrRunnerNotFound, node.Type)
	}
	timeout := time.Duration(execution.Run.Context.Nodes[execution.Node.Key].TimeoutNanos)
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runner.Run(runContext, execution)
}

// applyResultV0V1Frozen is immutable historical interpreter code. New profiles
// must add a new apply function rather than editing this migration-cut helper.
func (e *Engine) applyResultV0V1Frozen(ctx context.Context, runtime workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, lease Lease, execution Execution, result WorkerResult, validate profileValidateResultFunc, reconcile profileReconcileFunc) error {
	if result.Disposition == "" {
		result.Disposition = ResultComplete
	}
	if err := validate(ctx, e, runtime, run, definition, node, execution, &result); err != nil {
		return e.handleFailure(ctx, run, definition, node, lease, err)
	}
	now := e.now()
	builder := newMutationBuilder(e, run, now)
	expected := node.Status
	switch result.Disposition {
	case ResultWaitInput:
		node.Status = NodeWaitingInput
	case ResultWaitReview:
		node.Status = NodeWaitingReview
	case ResultComplete:
		node.Status = NodeCompleted
		node.CompletedAt = timePointer(now)
	default:
		return e.handleFailure(ctx, run, definition, node, lease, fmt.Errorf("unknown result disposition %q", result.Disposition))
	}
	node.LeaseOwner = ""
	node.LeaseExpiresAt = nil
	node.UpdatedAt = now
	if result.Manifest != nil {
		ref := result.Manifest.Ref()
		node.InputManifest = &ref
	}
	if result.Proposal != nil {
		ref := *result.Proposal
		node.OutputProposal = &ref
	}
	metadata := run.Context.Nodes[node.Key]
	metadata.Input = execution.Inputs.Canonical()
	if len(result.Output) > 0 {
		canonical, _ := domain.CanonicalJSON(result.Output)
		metadata.Output = canonical
	}
	if result.BuildManifest != nil {
		encoded, _ := domain.CanonicalJSON(result.BuildManifest)
		metadata.Output = encoded
		run.Context.Values["buildManifest"] = encoded
	}
	run.Context.Nodes[node.Key] = metadata
	builder.mark(node, expected, lease.WorkerID)
	eventActorID := ""
	eventPayload := map[string]any{"attempt": node.Attempt}
	if metadata.ExecutionActor != nil {
		eventActorID = metadata.ExecutionActor.ActorID
		for key, value := range provenanceEventPayload(*metadata.ExecutionActor) {
			eventPayload[key] = value
		}
	}
	builder.event("node."+string(node.Status), node.Key, eventPayload, eventActorID)
	if node.Status == NodeCompleted {
		definitionNode, _ := definition.FindNode(node.DefinitionNodeID)
		if definitionNode.Type == domain.NodeCondition {
			if err := applyConditionRoute(run, definition, node, result.Branch); err != nil {
				return err
			}
		}
		if definitionNode.Type == domain.NodeFanOut {
			if err := e.instantiateFanOut(run, definition, node, result.FanOutItems, builder); err != nil {
				return err
			}
		}
		if definitionNode.Type == domain.NodeMerge {
			e.cancelUnneededMergeBranches(run, definition, definitionNode, builder)
		}
	}
	reconcile(e, runtime, run, definition, builder)
	e.refreshRunStatus(run, definition, now)
	return e.Store.Commit(ctx, builder.build())
}

func (e *Engine) cancelUnneededMergeBranches(run *RunRecord, definition domain.WorkflowDefinition, merge domain.NodeDefinition, builder *mutationBuilder) {
	if merge.Merge == nil || merge.Merge.Policy == domain.MergeAll {
		return
	}
	region, err := definition.FanOutRegion(merge.Merge.FanOutNodeID)
	if err != nil {
		return
	}
	for _, slice := range slicesForFanOut(run.Context, merge.Merge.FanOutNodeID) {
		for _, definitionNodeID := range region {
			node := run.Nodes[instanceKey(definitionNodeID, slice.ID)]
			if node == nil || node.Status.Terminal() {
				continue
			}
			expected := node.Status
			node.Status = NodeCancelled
			node.LeaseOwner = ""
			node.LeaseExpiresAt = nil
			node.CompletedAt = timePointer(builder.now)
			node.UpdatedAt = builder.now
			builder.mark(node, expected, "")
		}
	}
}

// validateResultV0V1Frozen is immutable historical interpreter code. New
// validation behavior belongs in a new execution-profile implementation.
func (e *Engine) validateResultV0V1Frozen(ctx context.Context, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, execution Execution, result *WorkerResult) error {
	definitionNode, _ := definition.FindNode(node.DefinitionNodeID)
	if len(result.Output) > 0 {
		canonical, err := domain.CanonicalJSON(result.Output)
		if err != nil {
			return err
		}
		if err := validateNodeOutput(definitionNode, canonical); err != nil {
			return err
		}
		result.Output = canonical
	}
	switch definitionNode.Type {
	case domain.NodeAITransform, domain.NodeAI:
		if result.Manifest == nil && node.InputManifest == nil {
			return fmt.Errorf("AI/build nodes must freeze an input manifest")
		}
		if result.Manifest != nil {
			if err := result.Manifest.Validate(); err != nil {
				return err
			}
			if result.Manifest.ProjectID != run.ProjectID {
				return fmt.Errorf("manifest belongs to another project")
			}
			stored, loadErr := e.Store.GetManifest(ctx, result.Manifest.ID)
			switch {
			case loadErr == nil:
				if stored.Ref() != result.Manifest.Ref() {
					return fmt.Errorf("stored manifest pin mismatch")
				}
			case errors.Is(loadErr, domain.ErrNotFound):
				if err := e.Store.SaveManifest(ctx, *result.Manifest); err != nil && !errors.Is(err, ErrCASConflict) {
					return err
				}
			default:
				return loadErr
			}
		}
		if result.Proposal == nil {
			result.Disposition = ResultWaitInput
		} else {
			if err := result.Proposal.Validate(); err != nil {
				return err
			}
			proposal, err := e.Store.GetProposal(ctx, result.Proposal.ID)
			if err != nil {
				return err
			}
			manifestRef := node.InputManifest
			if result.Manifest != nil {
				ref := result.Manifest.Ref()
				manifestRef = &ref
			}
			if proposal.PayloadHash != result.Proposal.PayloadHash || manifestRef == nil || proposal.Manifest != *manifestRef {
				return fmt.Errorf("proposal is not pinned to the frozen node manifest")
			}
		}
	case domain.NodeWorkbenchBuild:
		if _, err := buildManifestFromExecution(execution); err != nil {
			return err
		}
		if len(result.Output) == 0 {
			return fmt.Errorf("workbench build must record implementation proposal references")
		}
	case domain.NodeCondition:
		if definitionNode.Condition == nil || !conditionHasBranch(definitionNode.Condition.Branches, result.Branch) {
			return fmt.Errorf("condition selected undeclared branch %q", result.Branch)
		}
	case domain.NodeFanOut:
		if len(result.FanOutItems) == 0 {
			return fmt.Errorf("fan-out produced no items")
		}
		limit, err := effectiveFanOutMaxItems(definitionNode.FanOut)
		if err != nil {
			return err
		}
		if len(result.FanOutItems) > limit {
			return fmt.Errorf("fan-out produced %d items, exceeding maxItems %d", len(result.FanOutItems), limit)
		}
	case domain.NodeManifestCompiler:
		if result.BuildManifest == nil {
			return fmt.Errorf("manifest compiler must return a build manifest")
		}
		if err := result.BuildManifest.Validate(); err != nil {
			return err
		}
		encoded, err := domain.CanonicalJSON(result.BuildManifest)
		if err != nil {
			return err
		}
		if err := validateNodeOutput(definitionNode, encoded); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) handleFailure(ctx context.Context, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, lease Lease, failure error) error {
	now := e.now()
	builder := newMutationBuilder(e, run, now)
	expected := node.Status
	metadata := run.Context.Nodes[node.Key]
	maxAttempts := metadata.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	node.Failure = mustJSON(map[string]any{"message": failure.Error(), "attempt": node.Attempt})
	node.LeaseOwner = ""
	node.LeaseExpiresAt = nil
	node.UpdatedAt = now
	if node.Attempt < maxAttempts {
		node.Status = NodeReady
		node.AvailableAt = now.Add(e.backoff(node.Attempt))
		builder.event("node.retry_scheduled", node.Key, map[string]any{"attempt": node.Attempt, "availableAt": node.AvailableAt, "error": failure.Error()}, "")
	} else {
		node.Status = NodeFailed
		node.CompletedAt = timePointer(now)
		builder.event("node.failed", node.Key, map[string]any{"attempt": node.Attempt, "error": failure.Error()}, "")
	}
	builder.mark(node, expected, lease.WorkerID)
	e.reconcile(run, definition, builder)
	e.refreshRunStatus(run, definition, now)
	if err := e.Store.Commit(ctx, builder.build()); err != nil {
		return err
	}
	return failure
}

// AuthorizeNodeExecution records an actor minted by the authenticated Facade
// and schedules a privileged automated node. No request payload is accepted,
// which keeps actor identity outside user-controlled workflow output JSON.
func (e *Engine) AuthorizeNodeExecution(ctx context.Context, runID, nodeKey string, actor ActorProvenance) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if actor.Source != ActorSourceAuthenticatedCommand {
		return core.ErrForbidden
	}
	run, record, node, definitionNode, err := e.loadExecution(ctx, runID, nodeKey)
	if err != nil {
		return err
	}
	action, requiredRole, required := nodeExecutionPolicy(definitionNode)
	if !required || node.Status != NodeWaitingInput {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "node is not waiting for privileged execution authorization"}
	}
	if actor.Action != action || !workflowRoleSatisfies(actor.Role, requiredRole) {
		return core.ErrForbidden
	}
	now := e.now()
	if actor.AuthorizedAt.After(now.Add(time.Minute)) {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "authorizedAt", Message: "execution authorization time is in the future"}
	}
	builder := newMutationBuilder(e, run, now)
	expected := node.Status
	metadata := run.Context.Nodes[node.Key]
	actorCopy := actor
	actorCopy.AuthorizedAt = actor.AuthorizedAt.UTC()
	metadata.ExecutionActor = &actorCopy
	run.Context.Nodes[node.Key] = metadata
	node.Status = NodeReady
	node.Failure = nil
	node.CompletedAt = nil
	node.AvailableAt = now
	node.UpdatedAt = now
	builder.mark(node, expected, "")
	builder.event("node.execution_authorized", node.Key, provenanceEventPayload(actorCopy), actorCopy.ActorID)
	e.reconcile(run, record.Definition, builder)
	e.refreshRunStatus(run, record.Definition, now)
	return e.Store.Commit(ctx, builder.build())
}

func (e *Engine) RecordProposal(ctx context.Context, runID, nodeKey string, proposalRef domain.ProposalRef, actorID string) error {
	run, record, node, definitionNode, err := e.loadExecution(ctx, runID, nodeKey)
	if err != nil {
		return err
	}
	if node.Status != NodeWaitingInput {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "node is not waiting for a proposal"}
	}
	if definitionNode.Type != domain.NodeAITransform && definitionNode.Type != domain.NodeAI && definitionNode.Type != domain.NodeWorkbenchBuild {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "node does not accept proposals"}
	}
	proposal, err := e.Store.GetProposal(ctx, proposalRef.ID)
	if err != nil {
		return err
	}
	if err := proposal.ValidatePayloadHash(); err != nil {
		return err
	}
	if proposal.PayloadHash != proposalRef.PayloadHash || node.InputManifest == nil || proposal.Manifest != *node.InputManifest {
		return fmt.Errorf("proposal is not pinned to the node input manifest")
	}
	now := e.now()
	builder := newMutationBuilder(e, run, now)
	expected := node.Status
	node.Status = NodeCompleted
	node.OutputProposal = &proposalRef
	node.CompletedAt = timePointer(now)
	node.UpdatedAt = now
	builder.mark(node, expected, "")
	builder.event("node.proposal_recorded", node.Key, map[string]any{"proposalId": proposalRef.ID}, actorID)
	e.reconcile(run, record.Definition, builder)
	e.refreshRunStatus(run, record.Definition, now)
	return e.Store.Commit(ctx, builder.build())
}

func (e *Engine) SubmitHumanInput(ctx context.Context, runID, nodeKey string, output json.RawMessage, actorID string) error {
	run, record, node, definitionNode, err := e.loadExecution(ctx, runID, nodeKey)
	if err != nil {
		return err
	}
	profile, err := e.executionProfile(run.ExecutionProfile)
	if err != nil {
		return err
	}
	runtime := profile.executionRuntime(e)
	if node.Status != NodeWaitingInput || (definitionNode.Type != domain.NodeHumanEdit && definitionNode.Type != domain.NodeHumanTask && definitionNode.Type != domain.NodeArtifactInput && definitionNode.Type != domain.NodeWorkbenchBuild) {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "node is not waiting for human input"}
	}
	canonical, err := domain.CanonicalJSON(output)
	if err != nil {
		return err
	}
	if err := validateNodeOutput(definitionNode, canonical); err != nil {
		return err
	}
	var outputRevisionID string
	var storedInputs domain.NodeInputEnvelope
	var hasStoredInputs bool
	if definitionNode.Type == domain.NodeWorkbenchBuild || definitionNode.Type == domain.NodeHumanEdit {
		storedInputs, hasStoredInputs, err = decodeStoredInputs(run.Context.Nodes[node.Key].Input)
		if err != nil {
			return err
		}
	}
	if definitionNode.Type == domain.NodeWorkbenchBuild {
		if runtime.workbenchCompletion == nil {
			return fmt.Errorf("workbench completion validator is required")
		}
		outputRevisionID, err = runtime.workbenchCompletion.ValidateCompletion(ctx, Execution{Run: *run, Node: *node, Definition: definitionNode, Inputs: storedInputs}, canonical)
		if err != nil {
			return err
		}
	}
	var humanEdit HumanEditValidation
	if definitionNode.Type == domain.NodeHumanEdit {
		if runtime.humanEditOutput == nil {
			return fmt.Errorf("human edit output validator is required")
		}
		if !hasStoredInputs && len(record.Definition.Incoming(definitionNode.ID)) > 0 {
			return &domain.DomainError{Kind: domain.ErrValidation, Field: "input", Message: "human edit input lineage is missing"}
		}
		humanEdit, err = runtime.humanEditOutput.ValidateHumanEdit(
			ctx,
			Execution{Run: *run, Node: *node, Definition: definitionNode, Inputs: storedInputs},
			canonical,
			actorID,
		)
		if err != nil {
			return err
		}
		if len(humanEdit.ArtifactRefs) == 0 || humanEdit.Primary.Validate() != nil || strings.TrimSpace(humanEdit.ArtifactKind) == "" {
			return &domain.DomainError{Kind: domain.ErrValidation, Field: "artifactRevision", Message: "human edit validator returned no exact artifact revision"}
		}
	}
	workflowContext, err := validatedHumanWorkflowContextWithKeys(definitionNode, canonical, runtime.humanContextKeys)
	if err != nil {
		return err
	}
	now := e.now()
	builder := newMutationBuilder(e, run, now)
	expected := node.Status
	metadata := run.Context.Nodes[node.Key]
	metadata.Output = canonical
	run.Context.Nodes[node.Key] = metadata
	if metadata.SliceID != "" && definitionNode.HumanEdit != nil && len(humanEdit.ArtifactRefs) > 0 {
		if err := applyHumanEditSliceLineage(run, metadata.SliceID, humanEdit); err != nil {
			return err
		}
	}
	for key, value := range workflowContext {
		run.Context.Values[key] = value
	}
	node.Status = NodeCompleted
	if outputRevisionID != "" {
		node.OutputRevisionID = outputRevisionID
	}
	node.CompletedAt = timePointer(now)
	node.UpdatedAt = now
	builder.mark(node, expected, "")
	builder.event("node.input_submitted", node.Key, nil, actorID)
	e.reconcile(run, record.Definition, builder)
	e.refreshRunStatus(run, record.Definition, now)
	return e.Store.Commit(ctx, builder.build())
}

func applyHumanEditSliceLineage(run *RunRecord, sliceID string, edit HumanEditValidation) error {
	if run == nil {
		return domain.ErrInvalidArgument
	}
	slice, exists := run.Context.Slices[sliceID]
	if !exists {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "slice", Message: "human edit references an unknown delivery slice"}
	}
	primary := edit.Primary
	switch edit.ArtifactKind {
	case "page_spec":
		slice.PageSpec = &primary
		slice.Prototype = nil
	case "prototype":
		slice.Prototype = &primary
	case "blueprint":
		slice.Blueprint = primary
		slice.PageSpec = nil
		slice.Prototype = nil
	}
	run.Context.Slices[sliceID] = slice
	return nil
}

func (e *Engine) validatedHumanWorkflowContext(
	definition domain.NodeDefinition,
	output json.RawMessage,
) (map[string]json.RawMessage, error) {
	return validatedHumanWorkflowContextWithKeys(definition, output, e.HumanWorkflowContextKeys)
}

func validatedHumanWorkflowContextWithKeys(
	definition domain.NodeDefinition,
	output json.RawMessage,
	allowedKeys map[string]struct{},
) (map[string]json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(output, &envelope); err != nil {
		return nil, nil
	}
	raw, exists := envelope["workflowContext"]
	if !exists || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, &domain.DomainError{Kind: domain.ErrValidation, Field: "workflowContext", Message: "must be an object"}
	}
	declared := declaredWorkflowContextKeys(definition)
	normalized := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		if strings.TrimSpace(key) == "" || len(key) > 128 {
			return nil, domain.ErrInvalidArgument
		}
		if reservedHumanWorkflowContextKey(key) {
			return nil, &domain.DomainError{Kind: domain.ErrValidation, Field: "workflowContext." + key, Message: "workflow context key is reserved for a trusted runner"}
		}
		_, schemaDeclared := declared[key]
		_, serverAllowed := allowedKeys[key]
		if !schemaDeclared && !serverAllowed {
			return nil, &domain.DomainError{Kind: domain.ErrValidation, Field: "workflowContext." + key, Message: "workflow context key is not declared by the node schema or server allowlist"}
		}
		canonical, err := domain.CanonicalJSON(value)
		if err != nil {
			return nil, err
		}
		normalized[key] = canonical
	}
	return normalized, nil
}

func declaredWorkflowContextKeys(definition domain.NodeDefinition) map[string]struct{} {
	result := map[string]struct{}{}
	ports, err := definition.ResolvedOutputPorts()
	if err != nil {
		return result
	}
	for _, port := range ports {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(port.Schema, &schema) != nil {
			continue
		}
		workflowContext, exists := schema.Properties["workflowContext"]
		if !exists {
			continue
		}
		var contextSchema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(workflowContext, &contextSchema) != nil {
			continue
		}
		for key := range contextSchema.Properties {
			result[key] = struct{}{}
		}
	}
	return result
}

func reservedHumanWorkflowContextKey(key string) bool {
	switch key {
	case "buildManifest", "workspaceRevision", "executionActor", "reviewDecisionActor", "deliverySlices":
		return true
	default:
		return strings.HasPrefix(key, "system.")
	}
}

type ReviewResolution string

const (
	ReviewApprove ReviewResolution = "approve"
	ReviewChanges ReviewResolution = "changes_requested"
	ReviewWaive   ReviewResolution = "waive"
)

func (e *Engine) ResolveReview(ctx context.Context, runID, nodeKey string, decision ReviewDecision) error {
	if err := decision.Actor.Validate(); err != nil {
		return err
	}
	expectedAction := core.ActionReview
	if decision.Resolution == ReviewApprove || decision.Resolution == ReviewWaive {
		expectedAction = core.ActionApprove
	}
	if decision.Actor.Action != expectedAction || decision.Actor.Source != ActorSourceAuthenticatedCommand {
		return core.ErrForbidden
	}
	resolution, reason, actorID := decision.Resolution, decision.Reason, decision.Actor.ActorID
	run, record, node, definitionNode, err := e.loadExecution(ctx, runID, nodeKey)
	if err != nil {
		return err
	}
	if node.Status != NodeWaitingReview {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "node is not waiting for review"}
	}
	profile, err := e.executionProfile(run.ExecutionProfile)
	if err != nil {
		return err
	}
	runtime := profile.executionRuntime(e)
	prohibitSelf, allowWaiver := false, false
	if definitionNode.ReviewGate != nil {
		prohibitSelf = definitionNode.ReviewGate.ProhibitSelfReview
		allowWaiver = definitionNode.ReviewGate.AllowWaiver
		if governedApplicationWorkflow(record.Definition) {
			allowWaiver = false
		}
	}
	if definitionNode.Approval != nil {
		prohibitSelf = definitionNode.Approval.ProhibitSelfReview
	}
	canonicalReviewVerified := false
	if resolution == ReviewApprove && definitionNode.Type == domain.NodeReviewGate && runtime.reviewGate != nil {
		inputs, hasInputs, inputErr := decodeStoredInputs(run.Context.Nodes[node.Key].Input)
		if inputErr != nil {
			return inputErr
		}
		refs := make([]domain.ArtifactRef, 0)
		if hasInputs {
			if governedApplicationWorkflow(record.Definition) {
				refs = append(refs, inputs.MaterializedArtifactRefs()...)
				// Compatibility for already-running governed nodes whose direct
				// HumanEdit predecessor predates the explicit materialized marker.
				if len(refs) == 0 {
					for _, binding := range inputs.Bindings() {
						if binding.Source.OutputRevisionID == "" {
							continue
						}
						for _, ref := range binding.Source.ArtifactRevisions {
							if ref.RevisionID == binding.Source.OutputRevisionID {
								refs = appendUniqueArtifactRef(refs, ref)
							}
						}
					}
				}
				if len(refs) != 1 {
					return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "review", Message: "governed review requires exactly one current HumanEdit materialization"}
				}
			} else {
				refs = append(refs, inputs.ArtifactRefs()...)
			}
		} else {
			if governedApplicationWorkflow(record.Definition) {
				return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "review", Message: "governed review requires a typed materialized input envelope"}
			}
			// Compatibility for runs that entered review before input envelopes
			// were introduced. New executions always use the pinned envelope.
			for _, predecessor := range effectiveIncoming(run, record.Definition, node) {
				if predecessor == nil || predecessor.Status != NodeCompleted {
					continue
				}
				found, parseErr := artifactRefsFromNodeOutput(run.Context.Nodes[predecessor.Key].Output)
				if parseErr != nil {
					return parseErr
				}
				refs = append(refs, found...)
			}
		}
		if err := runtime.reviewGate.VerifyApproval(ctx, run.ProjectID, refs, *definitionNode.ReviewGate); err != nil {
			return err
		}
		canonicalReviewVerified = true
	}
	if decision.SoloSelfReview {
		if resolution != ReviewApprove || actorID != run.StartedBy || run.GovernanceMode != core.GovernanceModeSolo || decision.GovernanceMode != core.GovernanceModeSolo {
			return core.ErrForbidden
		}
	}
	if prohibitSelf && !canonicalReviewVerified && actorID == run.StartedBy && !decision.SoloSelfReview {
		return domain.ErrSelfApproval
	}
	if (resolution == ReviewChanges || resolution == ReviewWaive) && strings.TrimSpace(reason) == "" {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "reason", Message: "review reason is required"}
	}
	if resolution == ReviewWaive && !allowWaiver {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "review", Message: "review gate does not allow waiver"}
	}
	now := e.now()
	if decision.Actor.AuthorizedAt.After(now.Add(time.Minute)) {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "authorizedAt", Message: "review authorization time is in the future"}
	}
	builder := newMutationBuilder(e, run, now)
	expected := node.Status
	metadata := run.Context.Nodes[node.Key]
	reviewActor := decision.Actor
	reviewActor.AuthorizedAt = reviewActor.AuthorizedAt.UTC()
	metadata.ReviewDecisionActor = &reviewActor
	switch resolution {
	case ReviewApprove:
		node.Status = NodeCompleted
		node.CompletedAt = timePointer(now)
	case ReviewWaive:
		node.Status = NodeCompleted
		node.CompletedAt = timePointer(now)
		metadata.Waived = true
		metadata.WaiverReason = reason
	case ReviewChanges:
		// Reopen the immediate producer. The gate becomes pending so it cannot be
		// approved again until a new exact artifact revision/proposal is supplied.
		node.Status = NodePending
		for _, predecessor := range effectiveIncoming(run, record.Definition, node) {
			if predecessor == nil || predecessor.Status != NodeCompleted {
				continue
			}
			predecessorDefinition, exists := record.Definition.FindNode(predecessor.DefinitionNodeID)
			if !exists {
				continue
			}
			expectedPredecessor := predecessor.Status
			switch predecessorDefinition.Type {
			case domain.NodeHumanEdit, domain.NodeHumanTask:
				predecessor.Status = NodeWaitingInput
			case domain.NodeAITransform, domain.NodeAI:
				predecessor.Status = NodeReady
				predecessor.InputManifest = nil
				predecessor.OutputProposal = nil
				predecessor.AvailableAt = now
			default:
				continue
			}
			predecessor.CompletedAt = nil
			predecessor.UpdatedAt = now
			predecessorMetadata := run.Context.Nodes[predecessor.Key]
			predecessorMetadata.Output = nil
			run.Context.Nodes[predecessor.Key] = predecessorMetadata
			builder.mark(predecessor, expectedPredecessor, "")
			builder.event("node.reopened_for_changes", predecessor.Key, map[string]any{"reviewNode": node.Key, "reason": reason}, actorID)
		}
	default:
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "resolution", Message: "unknown review resolution"}
	}
	if resolution == ReviewApprove || resolution == ReviewWaive {
		for successorKey, grant := range decision.ExecutionAuthorizations {
			if err := grant.Validate(); err != nil {
				return err
			}
			expectedSource := ActorSourceReviewApproval
			if resolution == ReviewWaive {
				expectedSource = ActorSourceReviewWaiver
			}
			if grant.ActorID != actorID || grant.Role != reviewActor.Role || grant.Source != expectedSource || !grant.AuthorizedAt.Equal(reviewActor.AuthorizedAt) {
				return core.ErrForbidden
			}
			successor := run.Nodes[successorKey]
			if successor == nil || successor.Status != NodePending {
				return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "executionAuthorizations", Message: "review handoff target is not pending"}
			}
			directSuccessor := false
			for _, edge := range record.Definition.Outgoing(node.DefinitionNodeID) {
				if edge.To == successor.DefinitionNodeID && !run.Context.DisabledEdges[disabledEdgeKey(edge.ID, node.SliceID)] {
					directSuccessor = true
					break
				}
			}
			if !directSuccessor {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "executionAuthorizations", Message: "review handoff must target a direct enabled successor"}
			}
			successorDefinition, exists := record.Definition.FindNode(successor.DefinitionNodeID)
			if !exists {
				return domain.ErrNotFound
			}
			action, role, required := nodeExecutionPolicy(successorDefinition)
			if !required || grant.Action != action || !workflowRoleSatisfies(grant.Role, role) {
				return core.ErrForbidden
			}
			grantCopy := grant
			grantCopy.AuthorizedAt = grantCopy.AuthorizedAt.UTC()
			successorMetadata := run.Context.Nodes[successorKey]
			successorMetadata.ExecutionActor = &grantCopy
			run.Context.Nodes[successorKey] = successorMetadata
			builder.event("node.execution_authorized", successorKey, provenanceEventPayload(grantCopy), actorID)
		}
	}
	node.UpdatedAt = now
	run.Context.Nodes[node.Key] = metadata
	builder.mark(node, expected, "")
	reviewPayload := provenanceEventPayload(reviewActor)
	reviewPayload["reason"] = reason
	reviewPayload["soloSelfReview"] = decision.SoloSelfReview
	reviewPayload["governanceMode"] = run.GovernanceMode
	builder.event("node.review_"+string(resolution), node.Key, reviewPayload, actorID)
	e.reconcile(run, record.Definition, builder)
	e.refreshRunStatus(run, record.Definition, now)
	return e.Store.Commit(ctx, builder.build())
}

func (e *Engine) WaiveNode(ctx context.Context, runID, nodeKey string, actor ActorProvenance, reason string) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if actor.Action != core.ActionApprove || actor.Source != ActorSourceAuthenticatedCommand {
		return core.ErrForbidden
	}
	if strings.TrimSpace(reason) == "" {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "reason", Message: "waiver reason is required"}
	}
	run, record, node, definitionNode, err := e.loadExecution(ctx, runID, nodeKey)
	if err != nil {
		return err
	}
	governedReleaseGate := definitionNode.Type == domain.NodeQualityGate && definitionNode.QualityGate != nil &&
		definitionNode.QualityGate.Blocking && definitionNode.QualityGate.GateName == "release" &&
		governedApplicationWorkflow(record.Definition)
	allowed := (definitionNode.Type == domain.NodeQualityGate && !governedReleaseGate) ||
		(definitionNode.Merge != nil && definitionNode.Merge.AllowWaiver) ||
		(definitionNode.ReviewGate != nil && definitionNode.ReviewGate.AllowWaiver)
	if !allowed {
		return fmt.Errorf("node does not allow waiver")
	}
	if node.Status != NodeFailed && node.Status != NodeWaitingReview && node.Status != NodeWaitingInput {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "node state cannot be waived"}
	}
	now := e.now()
	if actor.AuthorizedAt.After(now.Add(time.Minute)) {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "authorizedAt", Message: "waiver authorization time is in the future"}
	}
	builder := newMutationBuilder(e, run, now)
	expected := node.Status
	metadata := run.Context.Nodes[node.Key]
	metadata.Waived = true
	metadata.WaiverReason = reason
	actorCopy := actor
	actorCopy.AuthorizedAt = actorCopy.AuthorizedAt.UTC()
	metadata.ReviewDecisionActor = &actorCopy
	run.Context.Nodes[node.Key] = metadata
	node.Status = NodeCompleted
	node.CompletedAt = timePointer(now)
	node.UpdatedAt = now
	builder.mark(node, expected, "")
	payload := provenanceEventPayload(actorCopy)
	payload["reason"] = reason
	builder.event("node.waived", node.Key, payload, actorCopy.ActorID)
	e.reconcile(run, record.Definition, builder)
	e.refreshRunStatus(run, record.Definition, now)
	return e.Store.Commit(ctx, builder.build())
}

func governedApplicationWorkflow(definition domain.WorkflowDefinition) bool {
	return definition.InputContract != nil && definition.OutputContract != nil &&
		definition.OutputContract.Capability == domain.WorkflowOutputApplication
}

func (e *Engine) RetryNode(ctx context.Context, runID, nodeKey, actorID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "reason", Message: "retry reason is required"}
	}
	run, record, node, definitionNode, err := e.loadExecution(ctx, runID, nodeKey)
	if err != nil {
		return err
	}
	if node.Status != NodeFailed {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "only a failed node can be retried"}
	}
	now := e.now()
	builder := newMutationBuilder(e, run, now)
	expected := node.Status
	metadata := run.Context.Nodes[node.Key]
	metadata.MaxAttempts = max(metadata.MaxAttempts, node.Attempt+1)
	_, _, requiresActor := nodeExecutionPolicy(definitionNode)
	if requiresActor {
		metadata.ExecutionActor = nil
	}
	run.Context.Nodes[node.Key] = metadata
	if requiresActor {
		node.Status = NodeWaitingInput
	} else {
		node.Status = NodeReady
	}
	node.Attempt = 0
	node.Failure = nil
	node.CompletedAt = nil
	node.AvailableAt = now
	node.UpdatedAt = now
	run.Failure = nil
	run.CompletedAt = nil
	builder.mark(node, expected, "")
	builder.event("node.manual_retry", node.Key, map[string]any{"reason": reason}, actorID)
	if requiresActor {
		builder.event("node.execution_authorization_required", node.Key, nil, "")
	}
	e.reconcile(run, record.Definition, builder)
	e.refreshRunStatus(run, record.Definition, now)
	return e.Store.Commit(ctx, builder.build())
}

func (e *Engine) Cancel(ctx context.Context, runID, actorID, reason string) error {
	run, record, err := e.loadRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status.Terminal() {
		return ErrRunTerminal
	}
	if strings.TrimSpace(reason) == "" {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "reason", Message: "cancellation reason is required"}
	}
	now := e.now()
	builder := newMutationBuilder(e, run, now)
	for _, node := range run.Nodes {
		if node.Status.Terminal() {
			continue
		}
		expected := node.Status
		node.Status = NodeCancelled
		node.CompletedAt = timePointer(now)
		node.LeaseOwner = ""
		node.LeaseExpiresAt = nil
		node.UpdatedAt = now
		builder.mark(node, expected, "")
	}
	run.Status = RunCancelled
	run.CancelledAt = timePointer(now)
	run.CompletedAt = timePointer(now)
	builder.event("run.cancelled", "", map[string]any{"reason": reason}, actorID)
	_ = record
	return e.Store.Commit(ctx, builder.build())
}

func (e *Engine) loadRun(ctx context.Context, runID string) (*RunRecord, DefinitionRecord, error) {
	run, err := e.Store.GetRun(ctx, runID)
	if err != nil {
		return nil, DefinitionRecord{}, err
	}
	record, err := e.Store.GetDefinitionVersion(ctx, run.DefinitionVersionID)
	if err != nil {
		return nil, DefinitionRecord{}, err
	}
	if err := run.ExecutionProfile.Validate(); err != nil || record.ExecutionProfile != run.ExecutionProfile || run.Definition.ExecutionProfile != run.ExecutionProfile {
		return nil, DefinitionRecord{}, fmt.Errorf("run execution profile pin mismatch")
	}
	if err := ValidateDefinitionForExecutionProfile(record.Definition, record.ExecutionProfile); err != nil {
		return nil, DefinitionRecord{}, err
	}
	if _, err := e.executionProfile(run.ExecutionProfile); err != nil {
		return nil, DefinitionRecord{}, err
	}
	if record.Definition.RefForExecutionProfile(record.ExecutionProfile) != run.Definition {
		return nil, DefinitionRecord{}, fmt.Errorf("run definition pin mismatch")
	}
	return run, record, nil
}

func (e *Engine) loadExecution(ctx context.Context, runID, nodeKey string) (*RunRecord, DefinitionRecord, *NodeRecord, domain.NodeDefinition, error) {
	run, record, err := e.loadRun(ctx, runID)
	if err != nil {
		return nil, DefinitionRecord{}, nil, domain.NodeDefinition{}, err
	}
	node := run.Nodes[nodeKey]
	if node == nil {
		return nil, DefinitionRecord{}, nil, domain.NodeDefinition{}, domain.ErrNotFound
	}
	definition, exists := record.Definition.FindNode(node.DefinitionNodeID)
	if !exists {
		return nil, DefinitionRecord{}, nil, domain.NodeDefinition{}, fmt.Errorf("definition node %q missing", node.DefinitionNodeID)
	}
	return run, record, node, definition, nil
}

func (e *Engine) instantiateFanOut(run *RunRecord, definition domain.WorkflowDefinition, fanOut *NodeRecord, items []FanOutItem, builder *mutationBuilder) error {
	definitionNode, _ := definition.FindNode(fanOut.DefinitionNodeID)
	limit, err := effectiveFanOutMaxItems(definitionNode.FanOut)
	if err != nil {
		return err
	}
	if len(items) > limit {
		return fmt.Errorf("fan-out produced %d items, exceeding maxItems %d", len(items), limit)
	}
	region, err := definition.FanOutRegion(definitionNode.ID)
	if err != nil {
		return err
	}
	if len(region) == 0 {
		return fmt.Errorf("fan-out region is empty")
	}
	fanOutMetadata := run.Context.Nodes[fanOut.Key]
	if fanOutMetadata.FanOutOutputs == nil {
		fanOutMetadata.FanOutOutputs = map[string]json.RawMessage{}
	}
	seen := map[string]struct{}{}
	for _, item := range items {
		if strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("fan-out item key and title are required")
		}
		deliverySlice := definitionNode.FanOut != nil && definitionNode.FanOut.ItemKind == "delivery_slice"
		blueprintPage := definitionNode.FanOut != nil && (definitionNode.FanOut.ItemKind == "blueprint_page" || definitionNode.FanOut.ItemKind == "blueprint_selection_page")
		if deliverySlice || blueprintPage || item.Blueprint.ArtifactID != "" || item.Blueprint.RevisionID != "" || item.Blueprint.ContentHash != "" {
			if err := item.Blueprint.Validate(); err != nil {
				return fmt.Errorf("fan-out blueprint ref: %w", err)
			}
		}
		if blueprintPage && strings.TrimSpace(item.Blueprint.AnchorID) == "" {
			return fmt.Errorf("blueprint_page fan-out item requires an anchored Blueprint Page ref")
		}
		if deliverySlice && item.PageSpec == nil {
			return fmt.Errorf("delivery fan-out item requires an exact PageSpec ref")
		}
		if item.PageSpec != nil {
			if err := item.PageSpec.Validate(); err != nil {
				return fmt.Errorf("fan-out PageSpec ref: %w", err)
			}
		}
		if item.Prototype != nil {
			if err := item.Prototype.Validate(); err != nil {
				return fmt.Errorf("fan-out prototype ref: %w", err)
			}
		}
		if _, duplicate := seen[item.Key]; duplicate {
			return fmt.Errorf("fan-out item keys must be unique")
		}
		seen[item.Key] = struct{}{}
		sliceID := e.id()
		itemOutput := append(json.RawMessage(nil), item.Payload...)
		if len(itemOutput) == 0 {
			itemOutput, err = domain.CanonicalJSON(item)
			if err != nil {
				return err
			}
		}
		fanOutMetadata.FanOutOutputs[sliceID] = itemOutput
		sliceContext := SliceContext{ID: sliceID, Key: item.Key, Title: item.Title, FanOutNodeID: definitionNode.ID, Payload: itemOutput, Blueprint: item.Blueprint, OwnerID: item.OwnerID}
		if item.PageSpec != nil {
			ref := *item.PageSpec
			sliceContext.PageSpec = &ref
		}
		if item.Prototype != nil {
			ref := *item.Prototype
			sliceContext.Prototype = &ref
		}
		run.Context.Slices[sliceID] = sliceContext
		if item.Blueprint.Validate() == nil {
			builder.slices = append(builder.slices, sliceRecordFromContext(run.ProjectID, sliceContext, builder.now))
		}
		for _, definitionNodeID := range region {
			nodeDefinition, _ := definition.FindNode(definitionNodeID)
			key := instanceKey(definitionNodeID, sliceID)
			attempts, timeout := nodeLimits(nodeDefinition)
			node := &NodeRecord{ID: e.id(), RunID: run.ID, Key: key, DefinitionNodeID: definitionNodeID, SliceID: sliceID, Type: nodeDefinition.Type, Status: NodePending, AvailableAt: builder.now, CreatedAt: builder.now, UpdatedAt: builder.now}
			run.Nodes[key] = node
			run.Context.Nodes[key] = NodeMetadata{DefinitionNodeID: definitionNodeID, SliceID: sliceID, MaxAttempts: attempts, TimeoutNanos: int64(timeout)}
			builder.addNew(node)
		}
	}
	run.Context.Nodes[fanOut.Key] = fanOutMetadata
	builder.event("fan_out.created", fanOut.Key, map[string]any{"sliceCount": len(items)}, "")
	return nil
}

func (e *Engine) reconcile(run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	profile, err := e.executionProfile(run.ExecutionProfile)
	if err != nil {
		return
	}
	profile.reconcile(e, run, definition, builder)
}

// reconcileV0V1Frozen is immutable historical interpreter code. New scheduler
// semantics require a new profile and a new reconcile entry point.
func (e *Engine) reconcileV0V1Frozen(run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	for changed := true; changed; {
		changed = false
		if e.reconcileFanOutConcurrency(run, definition, builder) {
			changed = true
		}
		for _, node := range sortedNodes(run.Nodes) {
			if node.Status != NodePending {
				continue
			}
			definitionNode, exists := definition.FindNode(node.DefinitionNodeID)
			if !exists {
				continue
			}
			if node.SliceID != "" && dynamicRegionRoot(definition, definitionNode.ID) {
				// Root instances are activated only by the fan-out concurrency limiter.
				continue
			}
			if definitionNode.Type == domain.NodeMerge {
				ready, impossible := mergeOutcome(run, definition, definitionNode)
				if ready {
					expected := node.Status
					node.Status = NodeReady
					node.UpdatedAt = builder.now
					builder.mark(node, expected, "")
					builder.event("node.ready", node.Key, nil, "")
					changed = true
				}
				if impossible {
					expected := node.Status
					if definitionNode.Merge.AllowWaiver {
						node.Status = NodeWaitingReview
					} else {
						node.Status = NodeFailed
						node.CompletedAt = timePointer(builder.now)
					}
					node.UpdatedAt = builder.now
					builder.mark(node, expected, "")
					builder.event("merge.unsatisfied", node.Key, nil, "")
					changed = true
				}
				continue
			}
			incoming := effectiveIncoming(run, definition, node)
			if len(incoming) == 0 && len(definition.Incoming(node.DefinitionNodeID)) > 0 {
				expected := node.Status
				node.Status = NodeCancelled
				node.CompletedAt = timePointer(builder.now)
				node.UpdatedAt = builder.now
				builder.mark(node, expected, "")
				changed = true
				continue
			}
			allComplete := true
			for _, predecessor := range incoming {
				if predecessor == nil || predecessor.Status != NodeCompleted {
					allComplete = false
					break
				}
			}
			if allComplete {
				expected := node.Status
				metadata := run.Context.Nodes[node.Key]
				if _, _, required := nodeExecutionPolicy(definitionNode); required && metadata.ExecutionActor == nil {
					node.Status = NodeWaitingInput
					builder.event("node.execution_authorization_required", node.Key, nil, "")
				} else {
					node.Status = NodeReady
					builder.event("node.ready", node.Key, nil, "")
				}
				node.UpdatedAt = builder.now
				builder.mark(node, expected, "")
				changed = true
			}
		}
	}
	e.updateSliceStates(run, definition, builder)
}

// reconcileV2 preserves the frozen v0/v1 scheduler except for Condition-aware
// cancellation of a Merge whose paired FanOut can no longer be reached. Keep
// this entry point separate: persisted v0/v1 runs must retain their exact
// historical interpreter semantics.
func (e *Engine) reconcileV2(run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	for changed := true; changed; {
		changed = false
		if e.reconcileFanOutConcurrency(run, definition, builder) {
			changed = true
		}
		for _, node := range sortedNodes(run.Nodes) {
			if node.Status != NodePending {
				continue
			}
			definitionNode, exists := definition.FindNode(node.DefinitionNodeID)
			if !exists {
				continue
			}
			if node.SliceID != "" && dynamicRegionRoot(definition, definitionNode.ID) {
				// Root instances are activated only by the fan-out concurrency limiter.
				continue
			}
			if definitionNode.Type == domain.NodeMerge {
				if mergeFanOutRouteDisabled(run, definition, definitionNode) {
					expected := node.Status
					node.Status = NodeCancelled
					node.CompletedAt = timePointer(builder.now)
					node.UpdatedAt = builder.now
					builder.mark(node, expected, "")
					changed = true
					continue
				}
				ready, impossible := mergeOutcome(run, definition, definitionNode)
				if ready {
					expected := node.Status
					node.Status = NodeReady
					node.UpdatedAt = builder.now
					builder.mark(node, expected, "")
					builder.event("node.ready", node.Key, nil, "")
					changed = true
				}
				if impossible {
					expected := node.Status
					if definitionNode.Merge.AllowWaiver {
						node.Status = NodeWaitingReview
					} else {
						node.Status = NodeFailed
						node.CompletedAt = timePointer(builder.now)
					}
					node.UpdatedAt = builder.now
					builder.mark(node, expected, "")
					builder.event("merge.unsatisfied", node.Key, nil, "")
					changed = true
				}
				continue
			}
			incoming := effectiveIncoming(run, definition, node)
			if len(incoming) == 0 && len(definition.Incoming(node.DefinitionNodeID)) > 0 {
				expected := node.Status
				node.Status = NodeCancelled
				node.CompletedAt = timePointer(builder.now)
				node.UpdatedAt = builder.now
				builder.mark(node, expected, "")
				changed = true
				continue
			}
			allComplete := true
			for _, predecessor := range incoming {
				if predecessor == nil || predecessor.Status != NodeCompleted {
					allComplete = false
					break
				}
			}
			if allComplete {
				expected := node.Status
				metadata := run.Context.Nodes[node.Key]
				if _, _, required := nodeExecutionPolicy(definitionNode); required && metadata.ExecutionActor == nil {
					node.Status = NodeWaitingInput
					builder.event("node.execution_authorization_required", node.Key, nil, "")
				} else {
					node.Status = NodeReady
					builder.event("node.ready", node.Key, nil, "")
				}
				node.UpdatedAt = builder.now
				builder.mark(node, expected, "")
				changed = true
			}
		}
	}
	e.updateSliceStates(run, definition, builder)
}

// mergeFanOutRouteDisabled is deliberately based on the FanOut's effective
// predecessors, not merely its current status. A Pending FanOut with any
// still-effective predecessor may become runnable later and must keep its Merge
// alive. Conversely, Condition cancellation removes or cancels every effective
// predecessor, so a zero-slice Merge on that route can safely be cancelled and
// allow cancellation to propagate through downstream AND joins.
func mergeFanOutRouteDisabled(run *RunRecord, definition domain.WorkflowDefinition, merge domain.NodeDefinition) bool {
	if merge.Merge == nil || len(slicesForFanOut(run.Context, merge.Merge.FanOutNodeID)) != 0 {
		return false
	}
	fanOutDefinition, exists := definition.FindNode(merge.Merge.FanOutNodeID)
	if !exists || fanOutDefinition.Type != domain.NodeFanOut || len(definition.Incoming(fanOutDefinition.ID)) == 0 {
		return false
	}
	fanOut := run.Nodes[fanOutDefinition.ID]
	return fanOut != nil && len(effectiveIncoming(run, definition, fanOut)) == 0
}

func (e *Engine) reconcileFanOutConcurrency(run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) bool {
	changed := false
	for _, fanOut := range definition.Nodes {
		if fanOut.Type != domain.NodeFanOut || fanOut.FanOut == nil {
			continue
		}
		region, _ := definition.FanOutRegion(fanOut.ID)
		roots := regionRoots(definition, fanOut.ID, region)
		slices := slicesForFanOut(run.Context, fanOut.ID)
		active := 0
		for _, slice := range slices {
			if sliceActive(run, region, slice.ID) {
				active++
			}
		}
		for _, slice := range slices {
			if active >= fanOut.FanOut.MaxParallel {
				break
			}
			if sliceStarted(run, region, slice.ID) {
				continue
			}
			for _, root := range roots {
				key := instanceKey(root, slice.ID)
				node := run.Nodes[key]
				if node == nil || node.Status != NodePending {
					continue
				}
				expected := node.Status
				definitionNode, _ := definition.FindNode(node.DefinitionNodeID)
				metadata := run.Context.Nodes[node.Key]
				if _, _, required := nodeExecutionPolicy(definitionNode); required && metadata.ExecutionActor == nil {
					node.Status = NodeWaitingInput
					builder.event("node.execution_authorization_required", node.Key, map[string]any{"sliceId": slice.ID}, "")
				} else {
					node.Status = NodeReady
					builder.event("node.ready", node.Key, map[string]any{"sliceId": slice.ID}, "")
				}
				node.UpdatedAt = builder.now
				builder.mark(node, expected, "")
				changed = true
			}
			active++
		}
	}
	return changed
}

func (e *Engine) updateSliceStates(run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	for _, slice := range run.Context.Slices {
		if slice.Blueprint.Validate() != nil {
			continue
		}
		fanOut, _ := definition.FindNode(slice.FanOutNodeID)
		region, _ := definition.FanOutRegion(fanOut.ID)
		status := sliceWorkflowStatus(run, definition, region, slice.ID)
		record := sliceRecordFromContext(run.ProjectID, slice, builder.now)
		record.WorkflowStatus = status
		builder.upsertSlice(record)
	}
}

func (e *Engine) refreshRunStatus(run *RunRecord, definition domain.WorkflowDefinition, now time.Time) {
	terminalID, _ := definition.TerminalNodeID()
	terminal := run.Nodes[terminalID]
	if terminal != nil && terminal.Status == NodeCompleted {
		run.Status = RunCompleted
		run.CompletedAt = timePointer(now)
		return
	}
	for _, node := range run.Nodes {
		metadata := run.Context.Nodes[node.Key]
		if node.Status == NodeFailed && metadata.SliceID == "" {
			run.Status = RunFailed
			run.Failure = node.Failure
			run.CompletedAt = timePointer(now)
			return
		}
	}
	waitReview, waitInput, active := false, false, false
	for _, node := range run.Nodes {
		switch node.Status {
		case NodeWaitingReview:
			waitReview = true
		case NodeWaitingInput:
			waitInput = true
		case NodeReady, NodeRunning, NodePending:
			active = true
		}
	}
	switch {
	case waitInput:
		run.Status = RunWaitingInput
	case waitReview:
		run.Status = RunWaitingReview
	case active:
		run.Status = RunRunning
	default:
		run.Status = RunFailed
		run.Failure = mustJSON(map[string]any{"message": "workflow has no viable terminal path"})
		run.CompletedAt = timePointer(now)
	}
}

type mutationBuilder struct {
	engine   *Engine
	run      *RunRecord
	now      time.Time
	expected map[string]NodeStatus
	owners   map[string]string
	newKeys  map[string]bool
	slices   []SliceRecord
	events   []Event
}

func newMutationBuilder(engine *Engine, run *RunRecord, now time.Time) *mutationBuilder {
	return &mutationBuilder{engine: engine, run: run, now: now, expected: map[string]NodeStatus{}, owners: map[string]string{}, newKeys: map[string]bool{}}
}
func (b *mutationBuilder) addNew(node *NodeRecord) { b.newKeys[node.Key] = true }
func (b *mutationBuilder) mark(node *NodeRecord, expected NodeStatus, owner string) {
	if b.newKeys[node.Key] {
		return
	}
	if _, exists := b.expected[node.Key]; !exists {
		b.expected[node.Key] = expected
		b.owners[node.Key] = owner
	}
}
func (b *mutationBuilder) event(eventType, nodeKey string, payload any, actorID string) {
	raw := json.RawMessage(`{}`)
	if payload != nil {
		raw = mustJSON(payload)
	}
	b.events = append(b.events, Event{ID: b.engine.id(), RunID: b.run.ID, Type: eventType, NodeKey: nodeKey, Payload: raw, ActorID: actorID, CreatedAt: b.now})
}
func (b *mutationBuilder) upsertSlice(record SliceRecord) {
	for index := range b.slices {
		if b.slices[index].ID == record.ID {
			b.slices[index] = record
			return
		}
	}
	b.slices = append(b.slices, record)
}
func (b *mutationBuilder) build() RunMutation {
	nodes := make([]NodeMutation, 0, len(b.expected))
	keys := make([]string, 0, len(b.expected))
	for key := range b.expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		node := *cloneNodeRecord(b.run.Nodes[key])
		nodes = append(nodes, NodeMutation{Node: node, ExpectedStatus: b.expected[key], ExpectedOwner: b.owners[key]})
	}
	newKeys := make([]string, 0, len(b.newKeys))
	for key := range b.newKeys {
		newKeys = append(newKeys, key)
	}
	sort.Strings(newKeys)
	newNodes := make([]NodeRecord, 0, len(newKeys))
	for _, key := range newKeys {
		newNodes = append(newNodes, *cloneNodeRecord(b.run.Nodes[key]))
	}
	return RunMutation{RunID: b.run.ID, ExpectedCursor: b.run.EventCursor, Status: b.run.Status, Context: b.run.Context, Failure: b.run.Failure, CompletedAt: b.run.CompletedAt, CancelledAt: b.run.CancelledAt, Nodes: nodes, NewNodes: newNodes, Slices: append([]SliceRecord(nil), b.slices...), Events: append([]Event(nil), b.events...), UpdatedAt: b.now}
}

func applyConditionRoute(run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, branch string) error {
	definitionNode, _ := definition.FindNode(node.DefinitionNodeID)
	if definitionNode.Condition == nil || !conditionHasBranch(definitionNode.Condition.Branches, branch) {
		return fmt.Errorf("unknown condition branch %q", branch)
	}
	run.Context.SelectedBranches[node.Key] = branch
	metadata := run.Context.Nodes[node.Key]
	metadata.SelectedBranch = branch
	run.Context.Nodes[node.Key] = metadata
	for _, edge := range definition.Outgoing(definitionNode.ID) {
		port := edge.FromPort
		if port == "" {
			port = "default"
		}
		if port != branch {
			run.Context.DisabledEdges[disabledEdgeKey(edge.ID, node.SliceID)] = true
		}
	}
	return nil
}

func effectiveIncoming(run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord) []*NodeRecord {
	result := make([]*NodeRecord, 0)
	for _, edge := range definition.Incoming(node.DefinitionNodeID) {
		if run.Context.DisabledEdges[disabledEdgeKey(edge.ID, node.SliceID)] {
			continue
		}
		key := edge.From
		if node.SliceID != "" {
			fromMeta := run.Context.Nodes[instanceKey(edge.From, node.SliceID)]
			if fromMeta.DefinitionNodeID != "" {
				key = instanceKey(edge.From, node.SliceID)
			}
		}
		predecessor := run.Nodes[key]
		if predecessor != nil && predecessor.Status == NodeCancelled {
			continue
		}
		result = append(result, predecessor)
	}
	return result
}

func mergeOutcome(run *RunRecord, definition domain.WorkflowDefinition, merge domain.NodeDefinition) (bool, bool) {
	if merge.Merge == nil {
		return false, true
	}
	region, err := definition.FanOutRegion(merge.Merge.FanOutNodeID)
	if err != nil {
		return false, true
	}
	slices := slicesForFanOut(run.Context, merge.Merge.FanOutNodeID)
	if len(slices) == 0 {
		return false, false
	}
	successes, failures, pending := 0, 0, 0
	for _, slice := range slices {
		switch sliceWorkflowStatus(run, definition, region, slice.ID) {
		case "completed":
			successes++
		case "failed":
			failures++
		default:
			pending++
		}
	}
	required := len(slices)
	if merge.Merge.Policy == domain.MergeAny {
		required = 1
	}
	if merge.Merge.Policy == domain.MergeQuorum {
		required = merge.Merge.Quorum
	}
	if successes >= required {
		return true, false
	}
	if successes+pending < required || (pending == 0 && failures > 0) {
		return false, true
	}
	return false, false
}

func sliceWorkflowStatus(run *RunRecord, definition domain.WorkflowDefinition, region []string, sliceID string) string {
	terminals := regionTerminals(definition, region)
	completed := 0
	for _, definitionNodeID := range terminals {
		node := run.Nodes[instanceKey(definitionNodeID, sliceID)]
		if node == nil {
			return "pending"
		}
		switch node.Status {
		case NodeFailed, NodeStale:
			return "failed"
		case NodeCompleted, NodeCancelled:
			completed++
		default:
			return "in_progress"
		}
	}
	if completed == len(terminals) {
		return "completed"
	}
	return "in_progress"
}

func fanOutTemplateNodes(definition domain.WorkflowDefinition) (map[string]bool, error) {
	result := map[string]bool{}
	for _, node := range definition.Nodes {
		if node.Type != domain.NodeFanOut {
			continue
		}
		region, err := definition.FanOutRegion(node.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range region {
			if result[id] {
				return nil, fmt.Errorf("overlapping fan-out regions are not supported")
			}
			result[id] = true
		}
	}
	return result, nil
}
func regionRoots(definition domain.WorkflowDefinition, fanOutID string, region []string) []string {
	regionSet := stringSet(region)
	roots := make([]string, 0)
	for _, id := range region {
		root := true
		for _, edge := range definition.Incoming(id) {
			if regionSet[edge.From] {
				root = false
				break
			}
		}
		if root {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)
	return roots
}

func dynamicRegionRoot(definition domain.WorkflowDefinition, nodeID string) bool {
	for _, edge := range definition.Incoming(nodeID) {
		source, exists := definition.FindNode(edge.From)
		if exists && source.Type == domain.NodeFanOut {
			return true
		}
	}
	return false
}
func regionTerminals(definition domain.WorkflowDefinition, region []string) []string {
	regionSet := stringSet(region)
	terminals := make([]string, 0)
	for _, id := range region {
		terminal := true
		for _, edge := range definition.Outgoing(id) {
			if regionSet[edge.To] {
				terminal = false
				break
			}
		}
		if terminal {
			terminals = append(terminals, id)
		}
	}
	sort.Strings(terminals)
	return terminals
}
func slicesForFanOut(context RunContext, fanOutID string) []SliceContext {
	result := make([]SliceContext, 0)
	for _, slice := range context.Slices {
		if slice.FanOutNodeID == fanOutID {
			result = append(result, slice)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}
func sliceStarted(run *RunRecord, region []string, sliceID string) bool {
	for _, id := range region {
		node := run.Nodes[instanceKey(id, sliceID)]
		if node != nil && node.Status != NodePending {
			return true
		}
	}
	return false
}
func sliceActive(run *RunRecord, region []string, sliceID string) bool {
	for _, id := range region {
		node := run.Nodes[instanceKey(id, sliceID)]
		if node == nil {
			continue
		}
		if node.Status == NodeReady || node.Status == NodeRunning || node.Status == NodeWaitingInput || node.Status == NodeWaitingReview {
			return true
		}
	}
	return false
}
func sliceRecordFromContext(projectID string, slice SliceContext, now time.Time) SliceRecord {
	pageSpecRevisionID, prototypeRevisionID := "", ""
	if slice.PageSpec != nil {
		pageSpecRevisionID = slice.PageSpec.RevisionID
	}
	if slice.Prototype != nil {
		prototypeRevisionID = slice.Prototype.RevisionID
	}
	return SliceRecord{ID: slice.ID, ProjectID: projectID, Key: slice.Key, Title: slice.Title, BlueprintRevisionID: slice.Blueprint.RevisionID, PageSpecRevisionID: pageSpecRevisionID, PrototypeRevisionID: prototypeRevisionID, SyncStatus: "current", WorkflowStatus: "pending", OwnerID: slice.OwnerID, UpdatedAt: now}
}
func sortedNodes(nodes map[string]*NodeRecord) []*NodeRecord {
	result := make([]*NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}
func stringSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}
func instanceKey(definitionNodeID, sliceID string) string { return definitionNodeID + "@" + sliceID }
func disabledEdgeKey(edgeID, sliceID string) string {
	if sliceID == "" {
		return edgeID
	}
	return edgeID + "@" + sliceID
}
func conditionHasBranch(branches []domain.ConditionBranch, name string) bool {
	for _, branch := range branches {
		if branch.Name == name {
			return true
		}
	}
	return false
}
func nodeLimits(node domain.NodeDefinition) (int, time.Duration) {
	switch {
	case node.AITransform != nil:
		return node.AITransform.MaxAttempts, node.AITransform.Timeout
	case node.WorkbenchBuild != nil:
		return node.WorkbenchBuild.MaxAttempts, node.WorkbenchBuild.Timeout
	case node.AI != nil:
		return 3, 5 * time.Minute
	case node.Transform != nil, node.QualityGate != nil:
		return 3, 5 * time.Minute
	default:
		return 1, 5 * time.Minute
	}
}
func defaultRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 8)
	return delay
}
func (e *Engine) now() time.Time {
	if e.Clock == nil {
		return time.Now().UTC()
	}
	return e.Clock.Now().UTC()
}
func (e *Engine) id() string {
	if e.IDs == nil {
		return UUIDGenerator{}.NewID()
	}
	return e.IDs.NewID()
}
func (e *Engine) leaseDuration() time.Duration {
	if e.LeaseDuration <= 0 {
		return 2 * time.Minute
	}
	return e.LeaseDuration
}
func (e *Engine) backoff(attempt int) time.Duration {
	if e.RetryBackoff == nil {
		return defaultRetryBackoff(attempt)
	}
	return e.RetryBackoff(attempt)
}
func timePointer(value time.Time) *time.Time { copyValue := value.UTC(); return &copyValue }
