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
}

func NewEngine(store Store) (*Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("workflow store is required")
	}
	return &Engine{
		Store: store, IDs: UUIDGenerator{}, Clock: realClock{}, LeaseDuration: 2 * time.Minute,
		RetryBackoff: defaultRetryBackoff, Capabilities: PlatformWorkflowCapabilities(true, true),
	}, nil
}

type StartRequest struct {
	RunID               string
	ProjectID           string
	DefinitionVersionID string
	InputManifest       domain.ManifestRef
	Scope               json.RawMessage
	StartedBy           string
}

func (e *Engine) Start(ctx context.Context, request StartRequest) (*RunRecord, error) {
	definitionRecord, err := e.Store.GetDefinitionVersion(ctx, request.DefinitionVersionID)
	if err != nil {
		return nil, err
	}
	if !definitionRecord.Published {
		return nil, fmt.Errorf("workflow definition version is not published")
	}
	if definitionRecord.ProjectID != "" && definitionRecord.ProjectID != request.ProjectID {
		return nil, fmt.Errorf("workflow definition belongs to another project")
	}
	if err := definitionRecord.Definition.Validate(); err != nil {
		return nil, err
	}
	if definitionRecord.Definition.InputContract != nil {
		if err := e.Capabilities.ValidateDefinition(definitionRecord.Definition); err != nil {
			return nil, err
		}
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
	var artifactKinds []string
	if definitionRecord.Definition.InputContract != nil {
		if e.StartArtifactKinds == nil {
			return nil, fmt.Errorf("workflow start artifact-kind resolver is required")
		}
		artifactKinds, err = e.StartArtifactKinds.ResolveStartArtifactKinds(ctx, manifest)
		if err != nil {
			return nil, err
		}
	}
	if err := CompatibleStart(definitionRecord.Definition, DescribeStartManifest(manifest, artifactKinds), ""); err != nil {
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
	run := &RunRecord{ID: runID, ProjectID: request.ProjectID, DefinitionVersionID: request.DefinitionVersionID, Definition: definitionRecord.Definition.Ref(), InputManifest: &request.InputManifest, Status: RunRunning, Scope: cloneRaw(request.Scope), Context: contextState, StartedBy: request.StartedBy, StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now, Nodes: map[string]*NodeRecord{}}
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
	events := []Event{{ID: e.id(), RunID: run.ID, Type: "run.started", Payload: mustJSON(map[string]any{"definitionVersionId": request.DefinitionVersionID, "manifestId": request.InputManifest.ID}), ActorID: request.StartedBy, CreatedAt: now}}
	if entryNode := run.Nodes[entry]; entryNode != nil && entryNode.Status == NodeWaitingInput {
		events = append(events, Event{ID: e.id(), RunID: run.ID, Type: "node.execution_authorization_required", NodeKey: entry, Payload: json.RawMessage(`{}`), CreatedAt: now})
	}
	if err := e.Store.CreateRun(ctx, run, events); err != nil {
		return nil, err
	}
	return e.Store.GetRun(ctx, run.ID)
}

func (e *Engine) ClaimAndExecute(ctx context.Context, workerID string) error {
	lease, err := e.Store.ClaimRunnable(ctx, workerID, e.now(), e.leaseDuration())
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
	inputs, err := buildNodeInputEnvelope(run, definitionRecord.Definition, node)
	if err != nil {
		return e.handleFailure(ctx, run, definitionRecord.Definition, node, lease, err)
	}
	execution := Execution{Run: *run, Node: *node, Definition: definition, Workflow: definitionRecord.Definition, Lease: lease, Inputs: inputs}
	if _, _, required := nodeExecutionPolicy(definition); required && run.Context.Nodes[node.Key].ExecutionActor == nil {
		return e.applyResult(ctx, run, definitionRecord.Definition, node, lease, execution, WorkerResult{Disposition: ResultWaitInput})
	}
	result, runErr := e.executeNode(ctx, execution)
	if runErr != nil {
		return e.handleFailure(ctx, run, definitionRecord.Definition, node, lease, runErr)
	}
	return e.applyResult(ctx, run, definitionRecord.Definition, node, lease, execution, result)
}

func (e *Engine) executeNode(ctx context.Context, execution Execution) (WorkerResult, error) {
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
		if e.ArtifactInputs != nil {
			output, err = e.ArtifactInputs.Validate(ctx, execution, manifest)
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
		if e.ManifestFreezer != nil {
			manifest, err := e.ManifestFreezer.Freeze(ctx, execution)
			if err != nil {
				return WorkerResult{}, err
			}
			var proposal *domain.ProposalRef
			if e.ProposalDispatcher != nil {
				proposal, err = e.ProposalDispatcher.Dispatch(ctx, execution, manifest)
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
		if e.ManifestCompilers != nil {
			manifest, err := e.ManifestCompilers.Compile(ctx, execution)
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
		if execution.Workflow.InputContract == nil && e.BuildManifestHook != nil && execution.Definition.ManifestCompiler != nil {
			manifest, err := e.BuildManifestHook.Compile(ctx, execution)
			if err != nil {
				return WorkerResult{}, err
			}
			if err := manifest.Freeze(); err != nil {
				return WorkerResult{}, err
			}
			return WorkerResult{Disposition: ResultComplete, BuildManifest: &manifest}, nil
		}
	case domain.NodeCondition:
		if e.ConditionEvaluator != nil {
			branch, err := e.ConditionEvaluator.Evaluate(ctx, execution, node.Condition.Branches)
			if err != nil {
				return WorkerResult{}, err
			}
			return WorkerResult{Disposition: ResultComplete, Branch: branch}, nil
		}
	}
	if e.Runners == nil {
		return WorkerResult{}, fmt.Errorf("%w for %s", ErrRunnerNotFound, node.Type)
	}
	runner, exists := e.Runners.RunnerFor(node.Type)
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

func (e *Engine) applyResult(ctx context.Context, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, lease Lease, execution Execution, result WorkerResult) error {
	if result.Disposition == "" {
		result.Disposition = ResultComplete
	}
	if err := e.validateResult(ctx, run, definition, node, execution, &result); err != nil {
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
	e.reconcile(run, definition, builder)
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

func (e *Engine) validateResult(ctx context.Context, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, execution Execution, result *WorkerResult) error {
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
		if e.WorkbenchCompletion == nil {
			return fmt.Errorf("workbench completion validator is required")
		}
		outputRevisionID, err = e.WorkbenchCompletion.ValidateCompletion(ctx, Execution{Run: *run, Node: *node, Definition: definitionNode, Inputs: storedInputs}, canonical)
		if err != nil {
			return err
		}
	}
	var humanEdit HumanEditValidation
	if definitionNode.Type == domain.NodeHumanEdit {
		if e.HumanEditOutput == nil {
			return fmt.Errorf("human edit output validator is required")
		}
		if !hasStoredInputs && len(record.Definition.Incoming(definitionNode.ID)) > 0 {
			return &domain.DomainError{Kind: domain.ErrValidation, Field: "input", Message: "human edit input lineage is missing"}
		}
		humanEdit, err = e.HumanEditOutput.ValidateHumanEdit(
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
	workflowContext, err := e.validatedHumanWorkflowContext(definitionNode, canonical)
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
	case "prototype":
		slice.Prototype = &primary
	case "blueprint":
		slice.Blueprint = primary
	}
	run.Context.Slices[sliceID] = slice
	return nil
}

func (e *Engine) validatedHumanWorkflowContext(
	definition domain.NodeDefinition,
	output json.RawMessage,
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
		_, serverAllowed := e.HumanWorkflowContextKeys[key]
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
	prohibitSelf, allowWaiver := false, false
	if definitionNode.ReviewGate != nil {
		prohibitSelf = definitionNode.ReviewGate.ProhibitSelfReview
		allowWaiver = definitionNode.ReviewGate.AllowWaiver
	}
	if definitionNode.Approval != nil {
		prohibitSelf = definitionNode.Approval.ProhibitSelfReview
	}
	canonicalReviewVerified := false
	if resolution == ReviewApprove && definitionNode.Type == domain.NodeReviewGate && e.ReviewGate != nil {
		inputs, hasInputs, inputErr := decodeStoredInputs(run.Context.Nodes[node.Key].Input)
		if inputErr != nil {
			return inputErr
		}
		refs := make([]domain.ArtifactRef, 0)
		if hasInputs {
			refs = append(refs, inputs.ArtifactRefs()...)
		} else {
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
		if err := e.ReviewGate.VerifyApproval(ctx, run.ProjectID, refs, *definitionNode.ReviewGate); err != nil {
			return err
		}
		canonicalReviewVerified = true
	}
	if prohibitSelf && !canonicalReviewVerified && actorID == run.StartedBy {
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
	allowed := definitionNode.Type == domain.NodeQualityGate || (definitionNode.Merge != nil && definitionNode.Merge.AllowWaiver) || (definitionNode.ReviewGate != nil && definitionNode.ReviewGate.AllowWaiver)
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
	if record.Definition.Ref() != run.Definition {
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
