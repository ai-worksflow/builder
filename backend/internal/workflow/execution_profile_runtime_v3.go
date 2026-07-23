package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	maximumQualityResultV3Bytes     = 16 << 20
	maximumQualityManifestSlicesV3  = 1024
	maximumQualityManifestSourcesV3 = 2048
	maximumJavaScriptSafeIntegerV3  = int64(9007199254740991)
)

// qualifiedReleaseControllerRunnerV1 is deliberately package-private. A
// generic/legacy PublishRunner cannot accidentally satisfy this capability;
// the migration-84 implementation must opt in from this package after it owns
// the complete authorization/result authority described by the v3 profile.
type qualifiedReleaseControllerRunnerV1 interface {
	WorkerRunner
	workflowQualifiedReleaseControllerDispatchV1()
}

// qualifiedReleaseControllerRuntimeBinding is a sealed capability marker for
// the dedicated SQL-backed v3 worker. Shared Workflow workers exclude v3
// Publish at their Store boundary, so invoking this marker through generic
// runner dispatch is always a control-plane error.
type qualifiedReleaseControllerRuntimeBinding struct{}

func NewQualifiedReleaseControllerRuntimeBinding() WorkerRunner {
	return qualifiedReleaseControllerRuntimeBinding{}
}

func (qualifiedReleaseControllerRuntimeBinding) Run(context.Context, Execution) (WorkerResult, error) {
	return WorkerResult{}, &domain.DomainError{
		Kind: domain.ErrInvalidTransition, Field: "node",
		Message: "workflow-engine/v3 Publish is owned by the dedicated qualified Release Controller worker",
	}
}

func (qualifiedReleaseControllerRuntimeBinding) workflowQualifiedReleaseControllerDispatchV1() {}

func sealRuntimeV3(bootstrap workflowExecutionRuntime, descriptor WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error) {
	ref, err := descriptor.Ref()
	if err != nil || ref != WorkflowExecutionProfileV3Ref() || descriptor.Components != WorkflowExecutionProfileV3Descriptor().Components {
		return workflowExecutionRuntime{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.executionProfile", Message: "workflow-engine/v3 runtime identity drifted"}
	}
	runtime := cloneExecutionRuntime(bootstrap)
	runtime.conditionEvaluator = DeclarativeConditionEvaluatorV1{}
	if _, exists := runtime.runners[domain.NodeExternalQualificationGate]; exists {
		return workflowExecutionRuntime{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.executionProfile.runners.externalQualificationGate", Message: "external qualification gate cannot have a runner binding"}
	}
	publisher, exists := runtime.runners[domain.NodePublish]
	if !exists || nilWorkerRunnerV3(publisher) {
		return workflowExecutionRuntime{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.executionProfile.runners.publish", Message: "qualified release controller publisher capability is missing"}
	}
	if _, qualified := publisher.(qualifiedReleaseControllerRunnerV1); !qualified {
		return workflowExecutionRuntime{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.executionProfile.runners.publish", Message: "legacy Publish runner cannot own workflow-engine/v3 Publish"}
	}
	return runtime, nil
}

func nilWorkerRunnerV3(runner WorkerRunner) bool {
	if runner == nil {
		return true
	}
	value := reflect.ValueOf(runner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func buildNodeInputEnvelopeV3(run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord) (domain.NodeInputEnvelope, error) {
	envelope, err := buildNodeInputEnvelope(run, definition, node)
	if err != nil {
		return domain.NodeInputEnvelope{}, err
	}
	targetDefinition, exists := definition.FindNode(node.DefinitionNodeID)
	if !exists || targetDefinition.Type != domain.NodeExternalQualificationGate {
		return envelope, nil
	}
	if err := validateExternalQualificationTopologyV3(definition); err != nil {
		return domain.NodeInputEnvelope{}, err
	}
	bindings := envelope.Bindings()
	if len(bindings) != 1 {
		return domain.NodeInputEnvelope{}, qualificationInputErrorV3("external qualification gate requires exactly one Quality input binding")
	}
	binding := &bindings[0]
	sourceDefinition, exists := definition.FindNode(binding.Source.DefinitionNodeID)
	source := run.Nodes[binding.Source.NodeKey]
	if !exists || sourceDefinition.Type != domain.NodeQualityGate || source == nil || source.Type != domain.NodeQualityGate ||
		source.DefinitionNodeID != sourceDefinition.ID || source.Status != NodeCompleted || source.SliceID != "" ||
		binding.Source.RunID != run.ID || binding.Source.NodeKey != source.Key ||
		binding.FromPort != "default" || binding.ToPort != "default" || len(binding.Mapping) != 0 ||
		!bytes.Equal(binding.Output, binding.Value) {
		return domain.NodeInputEnvelope{}, qualificationInputErrorV3("external qualification gate input is not the exact identity-mapped completed Quality edge")
	}
	quality, err := decodeQualityResultV3(binding.Output, run)
	if err != nil {
		return domain.NodeInputEnvelope{}, err
	}
	if source.OutputRevisionID == "" || source.OutputRevisionID != quality.WorkspaceRevision.RevisionID ||
		binding.Source.OutputRevisionID != source.OutputRevisionID {
		return domain.NodeInputEnvelope{}, qualificationInputErrorV3("Quality output revision does not equal its WorkspaceRevision")
	}
	binding.Source.ArtifactRevisions = appendUniqueArtifactRef(binding.Source.ArtifactRevisions, *quality.WorkspaceRevision)
	envelope, err = domain.NewNodeInputEnvelope(bindings)
	if err != nil {
		return domain.NodeInputEnvelope{}, err
	}
	if canonical := envelope.Canonical(); len(canonical) == 0 || len(canonical) > maximumQualityResultV3Bytes {
		return domain.NodeInputEnvelope{}, qualificationInputErrorV3("external qualification gate input exceeds the 16 MiB Workflow Input Authority limit")
	}
	publishInput, err := workflowPublishInputFromExecution(Execution{
		Run: *run, Node: *node, Definition: targetDefinition, Workflow: definition, Inputs: envelope,
	})
	if err != nil {
		return domain.NodeInputEnvelope{}, qualificationInputErrorV3("external qualification gate input is invalid: %v", err)
	}
	manifestBytes, manifestErr := domain.CanonicalJSON(publishInput.BuildManifest)
	qualityManifestBytes, qualityManifestErr := domain.CanonicalJSON(quality.BuildManifest)
	if manifestErr != nil || qualityManifestErr != nil || publishInput.QualityRunID != quality.QualityRunID ||
		!publishInput.WorkspaceRevision.Equal(*quality.WorkspaceRevision) || !bytes.Equal(manifestBytes, qualityManifestBytes) {
		return domain.NodeInputEnvelope{}, qualificationInputErrorV3("external qualification gate input does not contain one exact passing QualityResult")
	}
	return envelope, nil
}

func executeNodeV3(ctx context.Context, engine *Engine, runtime workflowExecutionRuntime, execution Execution) (WorkerResult, error) {
	switch execution.Definition.Type {
	case domain.NodeExternalQualificationGate:
		return WorkerResult{}, &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "external qualification gate is controlled only by canonical qualification authority protocols"}
	case domain.NodePublish:
		return WorkerResult{}, &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "workflow-engine/v3 qualified release execution authority is not enabled"}
	default:
		return engine.executeNodeV0V1Frozen(ctx, runtime, execution)
	}
}

func validateResultV3(ctx context.Context, engine *Engine, _ workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, node *NodeRecord, execution Execution, result *WorkerResult) error {
	definitionNode, exists := definition.FindNode(node.DefinitionNodeID)
	if !exists || definitionNode.Type != node.Type {
		return qualificationInputErrorV3("workflow node does not match its frozen definition")
	}
	switch definitionNode.Type {
	case domain.NodeExternalQualificationGate:
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "external qualification gate has no generic result path"}
	case domain.NodePublish:
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "workflow-engine/v3 qualified release result authority is not enabled"}
	case domain.NodeQualityGate:
		if result.Disposition != ResultComplete {
			return qualificationInputErrorV3("blocking release Quality must complete with one passing QualityResult")
		}
		if err := execution.Inputs.Validate(); err != nil {
			return qualificationInputErrorV3("blocking release Quality input envelope is invalid: %v", err)
		}
		if _, err := decodeQualityResultV3(result.Output, run); err != nil {
			return err
		}
	}
	return engine.validateResultV0V1Frozen(ctx, run, definition, node, execution, result)
}

func applyResultV3(
	ctx context.Context,
	engine *Engine,
	runtime workflowExecutionRuntime,
	run *RunRecord,
	definition domain.WorkflowDefinition,
	node *NodeRecord,
	lease Lease,
	execution Execution,
	result WorkerResult,
) error {
	if engine == nil || run == nil || node == nil {
		return domain.ErrInvalidArgument
	}
	definitionNode, exists := definition.FindNode(node.DefinitionNodeID)
	if !exists || definitionNode.Type != node.Type {
		return qualificationInputErrorV3("workflow node does not match its frozen definition")
	}
	if definitionNode.Type == domain.NodeExternalQualificationGate || definitionNode.Type == domain.NodePublish {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "dedicated workflow-engine/v3 authority cannot use generic result apply"}
	}
	if !nodeMatchesLease(node, lease) || execution.Run.ID != run.ID || execution.Node.ID != node.ID || execution.Lease != lease {
		return ErrLeaseLost
	}
	if result.Disposition == "" {
		result.Disposition = ResultComplete
	}
	isQuality := definitionNode.Type == domain.NodeQualityGate
	if err := validateResultV3(ctx, engine, runtime, run, definition, node, execution, &result); err != nil {
		if isQuality {
			return err
		}
		return engine.handleFailure(ctx, run, definition, node, lease, err)
	}
	if isQuality {
		qualityMetadata, metadataExists := run.Context.Nodes[node.Key]
		if !metadataExists || qualityMetadata.DefinitionNodeID != definitionNode.ID ||
			node.OutputRevisionID != "" || node.CompletedAt != nil || len(node.Failure) != 0 || len(qualityMetadata.Output) != 0 {
			return qualificationInputErrorV3("Quality node is not in a pristine running result state")
		}
		if err := validateExternalQualificationTopologyV3(definition); err != nil {
			return err
		}
		if _, _, err := pendingExternalQualificationGateV3(run, definition); err != nil {
			return err
		}
	}

	// Build the complete mutation on a detached aggregate. Failed validation,
	// envelope construction, or Store.Commit therefore cannot leak a partial
	// Quality completion or gate input into the caller's run snapshot.
	working := cloneRunRecord(run)
	working.Context.ensureMaps()
	workingNode := working.Nodes[node.Key]
	if workingNode == nil || !nodeMatchesLease(workingNode, lease) {
		return ErrLeaseLost
	}
	// PostgreSQL timestamptz and the v3 authority wires share a millisecond
	// boundary. Truncating once keeps node/event/outbox/precommit timestamps
	// identical instead of allowing sub-millisecond driver rounding to drift.
	now := engine.now().Truncate(time.Millisecond)
	builder := newMutationBuilder(engine, working, now)
	expected := workingNode.Status
	switch result.Disposition {
	case ResultWaitInput:
		workingNode.Status = NodeWaitingInput
	case ResultWaitReview:
		workingNode.Status = NodeWaitingReview
	case ResultComplete:
		workingNode.Status = NodeCompleted
		workingNode.CompletedAt = timePointer(now)
	default:
		if isQuality {
			return qualificationInputErrorV3("unknown Quality result disposition %q", result.Disposition)
		}
		return engine.handleFailure(ctx, run, definition, node, lease, fmt.Errorf("unknown result disposition %q", result.Disposition))
	}
	workingNode.LeaseOwner = ""
	workingNode.LeaseExpiresAt = nil
	workingNode.UpdatedAt = now
	if result.Manifest != nil {
		ref := result.Manifest.Ref()
		workingNode.InputManifest = &ref
	}
	if result.Proposal != nil {
		ref := *result.Proposal
		workingNode.OutputProposal = &ref
	}
	metadata := working.Context.Nodes[workingNode.Key]
	metadata.Input = execution.Inputs.Canonical()
	if len(result.Output) > 0 {
		canonical, err := domain.CanonicalJSON(result.Output)
		if err != nil {
			return err
		}
		metadata.Output = canonical
	}
	if result.BuildManifest != nil {
		encoded, err := domain.CanonicalJSON(result.BuildManifest)
		if err != nil {
			return err
		}
		metadata.Output = encoded
		working.Context.Values["buildManifest"] = encoded
	}
	working.Context.Nodes[workingNode.Key] = metadata

	var qualityGate *NodeRecord
	var qualityGateInput domain.NodeInputEnvelope
	if isQuality {
		quality, err := decodeQualityResultV3(metadata.Output, working)
		if err != nil {
			return err
		}
		// WIA locks and checks this column against the exact source projection in
		// the gate envelope. It is derived only from the strict v3 QualityResult.
		workingNode.OutputRevisionID = quality.WorkspaceRevision.RevisionID
		_, gate, err := pendingExternalQualificationGateV3(working, definition)
		if err != nil {
			return err
		}
		gateInput, err := buildNodeInputEnvelopeV3(working, definition, gate)
		if err != nil {
			return err
		}
		gateMetadata := working.Context.Nodes[gate.Key]
		if len(gateMetadata.Input) != 0 {
			return qualificationInputErrorV3("external qualification gate typed input already exists")
		}
		gateMetadata.Input = gateInput.Canonical()
		working.Context.Nodes[gate.Key] = gateMetadata
		qualityGate = gate
		qualityGateInput = gateInput
	}

	builder.mark(workingNode, expected, lease.WorkerID)
	eventActorID := ""
	eventPayload := map[string]any{"attempt": workingNode.Attempt}
	if metadata.ExecutionActor != nil {
		eventActorID = metadata.ExecutionActor.ActorID
		for key, value := range provenanceEventPayload(*metadata.ExecutionActor) {
			eventPayload[key] = value
		}
	}
	completionEvent := builder.event("node."+string(workingNode.Status), workingNode.Key, eventPayload, eventActorID)
	if isQuality {
		precommit, err := newQualityCompletionPrecommitMutation(
			engine.id(), engine.id(), engine.id(), engine.id(),
			working, workingNode, qualityGate, lease, completionEvent, qualityGateInput,
		)
		if err != nil {
			return err
		}
		builder.setQualityCompletionPrecommit(precommit)
	}
	if workingNode.Status == NodeCompleted {
		switch definitionNode.Type {
		case domain.NodeCondition:
			if err := applyConditionRoute(working, definition, workingNode, result.Branch); err != nil {
				return err
			}
		case domain.NodeFanOut:
			if err := engine.instantiateFanOut(working, definition, workingNode, result.FanOutItems, builder); err != nil {
				return err
			}
		case domain.NodeMerge:
			engine.cancelUnneededMergeBranches(working, definition, definitionNode, builder)
		}
	}
	reconcileV3(engine, runtime, working, definition, builder)
	engine.refreshRunStatus(working, definition, now)
	return engine.Store.Commit(ctx, builder.build())
}

func pendingExternalQualificationGateV3(run *RunRecord, definition domain.WorkflowDefinition) (domain.NodeDefinition, *NodeRecord, error) {
	var gateDefinition domain.NodeDefinition
	definitionCount := 0
	for _, candidate := range definition.Nodes {
		if candidate.Type == domain.NodeExternalQualificationGate {
			gateDefinition = candidate
			definitionCount++
		}
	}
	if definitionCount != 1 {
		return domain.NodeDefinition{}, nil, qualificationInputErrorV3("workflow-engine/v3 requires exactly one external qualification gate")
	}
	var gate *NodeRecord
	for _, candidate := range run.Nodes {
		if candidate != nil && candidate.DefinitionNodeID == gateDefinition.ID {
			if gate != nil {
				return domain.NodeDefinition{}, nil, qualificationInputErrorV3("workflow run contains multiple external qualification gate instances")
			}
			gate = candidate
		}
	}
	if gate == nil || gate.Key != gateDefinition.ID || gate.RunID != run.ID || gate.Type != domain.NodeExternalQualificationGate ||
		gate.SliceID != "" || gate.Status != NodePending || gate.Attempt != 0 || gate.InputAuthorityID != "" ||
		gate.LeaseOwner != "" || gate.LeaseExpiresAt != nil || gate.StartedAt != nil || gate.CompletedAt != nil ||
		gate.InputManifest != nil || gate.OutputProposal != nil || gate.OutputRevisionID != "" || len(gate.Failure) != 0 {
		return domain.NodeDefinition{}, nil, qualificationInputErrorV3("external qualification gate is not in its pristine pending state")
	}
	metadata, exists := run.Context.Nodes[gate.Key]
	if !exists || metadata.DefinitionNodeID != gateDefinition.ID || metadata.SliceID != "" || len(metadata.Input) != 0 ||
		len(metadata.Output) != 0 || metadata.Waived || metadata.WaiverReason != "" || metadata.SelectedBranch != "" ||
		len(metadata.FanOutOutputs) != 0 || metadata.ExecutionActor != nil || metadata.ReviewDecisionActor != nil {
		return domain.NodeDefinition{}, nil, qualificationInputErrorV3("external qualification gate context is not pristine")
	}
	return gateDefinition, gate, nil
}

func reconcileV3(engine *Engine, _ workflowExecutionRuntime, run *RunRecord, definition domain.WorkflowDefinition, builder *mutationBuilder) {
	for changed := true; changed; {
		changed = false
		if engine.reconcileFanOutConcurrency(run, definition, builder) {
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
			// These states are advanced only by WIA, Handoff, and migration-84.
			// Generic predecessor completion must never emit ready events for them.
			if definitionNode.Type == domain.NodeExternalQualificationGate || definitionNode.Type == domain.NodePublish {
				continue
			}
			if node.SliceID != "" && dynamicRegionRoot(definition, definitionNode.ID) {
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
	engine.updateSliceStates(run, definition, builder)
}

type qualityFindingsV3 struct {
	Checks            json.RawMessage           `json:"checks"`
	Diagnostics       json.RawMessage           `json:"diagnostics"`
	QualityRunID      string                    `json:"qualityRunId"`
	ReportArtifactID  string                    `json:"reportArtifactId"`
	ReportRevisionID  string                    `json:"reportRevisionId"`
	Score             int64                     `json:"score"`
	WorkspaceRevision qualityWorkspaceVersionV3 `json:"workspaceRevision"`
}

type qualityWorkspaceVersionV3 struct {
	ArtifactID  string  `json:"artifactId"`
	RevisionID  string  `json:"revisionId"`
	ContentHash string  `json:"contentHash"`
	AnchorID    *string `json:"anchorId,omitempty"`
}

func decodeQualityResultV3(raw json.RawMessage, run *RunRecord) (QualityResult, error) {
	if run == nil || len(raw) == 0 || len(raw) > maximumQualityResultV3Bytes || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return QualityResult{}, qualificationInputErrorV3("QualityResult must be bounded BOM-free UTF-8 JSON")
	}
	if err := rejectDuplicateJSONNamesV3(raw); err != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult: %v", err)
	}
	if err := requireExactObjectFieldsV3(raw,
		[]string{"passed", "findings", "qualityRunId", "workspaceRevision", "buildManifest"}, nil); err != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult: %v", err)
	}
	var result QualityResult
	if err := strictDecodeJSONV3(raw, &result); err != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult strict decode: %v", err)
	}
	if !result.Passed || !canonicalUUIDv4V3(result.QualityRunID) || result.WorkspaceRevision == nil || result.BuildManifest == nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult must be passing and contain exact run/workspace/manifest identities")
	}
	workspace := *result.WorkspaceRevision
	if !canonicalUUIDv4V3(workspace.ArtifactID) || !canonicalUUIDv4V3(workspace.RevisionID) || workspace.AnchorID != "" || workspace.Validate() != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult WorkspaceRevision is invalid")
	}
	workspaceRaw, err := objectFieldV3(raw, "workspaceRevision")
	if err != nil {
		return QualityResult{}, err
	}
	if err := requireExactObjectFieldsV3(workspaceRaw, []string{"artifactId", "revisionId", "contentHash"}, nil); err != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult WorkspaceRevision: %v", err)
	}
	manifestRaw, err := objectFieldV3(raw, "buildManifest")
	if err != nil {
		return QualityResult{}, err
	}
	if err := requireExactObjectFieldsV3(manifestRaw,
		[]string{"schemaVersion", "projectId", "runId", "manifestGroupKey", "sliceIds", "bundleIds", "sources", "constraints", "createdAt", "hash"}, nil); err != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest: %v", err)
	}
	manifest := *result.BuildManifest
	if manifest.Validate() != nil || manifest.ProjectID != run.ProjectID || manifest.RunID != run.ID ||
		manifest.SchemaVersion < 1 || int64(manifest.SchemaVersion) > maximumJavaScriptSafeIntegerV3 ||
		len(manifest.SliceIDs) < 1 || len(manifest.SliceIDs) > maximumQualityManifestSlicesV3 ||
		len(manifest.BundleIDs) != len(manifest.SliceIDs) || len(manifest.Sources) < 1 ||
		len(manifest.Sources) > maximumQualityManifestSourcesV3 ||
		!canonicalUUIDv4V3(manifest.ProjectID) || !canonicalUUIDv4V3(manifest.RunID) ||
		!canonicalUUIDv4V3(manifest.ManifestGroupKey) || manifest.CreatedAt.IsZero() || manifest.CreatedAt.Location() != time.UTC {
		return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest does not match the workflow run")
	}
	createdAtRaw, err := objectFieldV3(manifestRaw, "createdAt")
	if err != nil {
		return QualityResult{}, err
	}
	canonicalCreatedAt, err := manifest.CreatedAt.MarshalJSON()
	if err != nil || !bytes.Equal(bytes.TrimSpace(createdAtRaw), canonicalCreatedAt) {
		return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest createdAt must use canonical UTC RFC3339Nano form")
	}
	seenSlices, seenBundles, seenSources := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index := range manifest.SliceIDs {
		if !canonicalUUIDv4V3(manifest.SliceIDs[index]) || !canonicalUUIDv4V3(manifest.BundleIDs[index]) ||
			seenSlices[manifest.SliceIDs[index]] || seenBundles[manifest.BundleIDs[index]] {
			return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest slice/bundle identities are invalid")
		}
		seenSlices[manifest.SliceIDs[index]], seenBundles[manifest.BundleIDs[index]] = true, true
	}
	for _, source := range manifest.Sources {
		key := source.ArtifactID + "\x00" + source.RevisionID + "\x00" + source.ContentHash + "\x00" + source.AnchorID
		if source.Validate() != nil || !canonicalUUIDv4V3(source.ArtifactID) || !canonicalUUIDv4V3(source.RevisionID) || seenSources[key] {
			return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest sources are invalid or duplicated")
		}
		seenSources[key] = true
	}
	sourcesRaw, err := objectFieldV3(manifestRaw, "sources")
	if err != nil {
		return QualityResult{}, err
	}
	var rawSources []json.RawMessage
	if err := strictDecodeJSONV3(sourcesRaw, &rawSources); err != nil || len(rawSources) != len(manifest.Sources) {
		return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest sources are not an exact array")
	}
	for index, rawSource := range rawSources {
		if err := requireExactObjectFieldsV3(rawSource, []string{"artifactId", "revisionId", "contentHash"}, []string{"anchorId"}); err != nil {
			return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest source %d: %v", index, err)
		}
		var sourceObject map[string]json.RawMessage
		if err := json.Unmarshal(rawSource, &sourceObject); err != nil {
			return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest source %d is invalid", index)
		}
		if rawAnchor, exists := sourceObject["anchorId"]; exists {
			var anchor string
			if strictDecodeJSONV3(rawAnchor, &anchor) != nil || anchor == "" {
				return QualityResult{}, qualificationInputErrorV3("QualityResult BuildManifest source %d anchorId must be a non-empty string", index)
			}
		}
	}
	findingsRaw, err := objectFieldV3(raw, "findings")
	if err != nil {
		return QualityResult{}, err
	}
	if err := requireExactObjectFieldsV3(findingsRaw,
		[]string{"checks", "diagnostics", "qualityRunId", "reportArtifactId", "reportRevisionId", "score", "workspaceRevision"}, nil); err != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult findings: %v", err)
	}
	var findings qualityFindingsV3
	if err := strictDecodeJSONV3(findingsRaw, &findings); err != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult findings strict decode: %v", err)
	}
	if !explicitJSONArrayV3(findings.Checks) || !explicitJSONArrayV3(findings.Diagnostics) ||
		findings.QualityRunID != result.QualityRunID || !canonicalUUIDv4V3(findings.ReportArtifactID) ||
		!canonicalUUIDv4V3(findings.ReportRevisionID) || findings.Score < 0 || findings.Score > 100 ||
		findings.WorkspaceRevision.AnchorID != nil || findings.WorkspaceRevision.ArtifactID != workspace.ArtifactID ||
		findings.WorkspaceRevision.RevisionID != workspace.RevisionID || findings.WorkspaceRevision.ContentHash != workspace.ContentHash {
		return QualityResult{}, qualificationInputErrorV3("QualityResult findings do not repeat the exact Quality run and WorkspaceRevision")
	}
	findingsWorkspaceRaw, err := objectFieldV3(findingsRaw, "workspaceRevision")
	if err != nil {
		return QualityResult{}, err
	}
	if err := requireExactObjectFieldsV3(findingsWorkspaceRaw,
		[]string{"artifactId", "revisionId", "contentHash"}, nil); err != nil {
		return QualityResult{}, qualificationInputErrorV3("QualityResult findings WorkspaceRevision: %v", err)
	}
	return result, nil
}

func qualificationInputErrorV3(format string, args ...any) error {
	return &domain.DomainError{Kind: domain.ErrValidation, Field: "qualityResult", Message: fmt.Sprintf(format, args...)}
}

func canonicalUUIDv4V3(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122
}

func explicitJSONArrayV3(raw json.RawMessage) bool {
	var value any
	if strictDecodeJSONV3(raw, &value) != nil {
		return false
	}
	_, ok := value.([]any)
	return ok
}

func objectFieldV3(raw json.RawMessage, field string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, qualificationInputErrorV3("field %s is not in an object", field)
	}
	value, exists := object[field]
	if !exists || len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, qualificationInputErrorV3("field %s is required and cannot be null", field)
	}
	return value, nil
}

func requireExactObjectFieldsV3(raw json.RawMessage, required, optional []string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("must be an object")
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, field := range required {
		allowed[field] = true
		value, exists := object[field]
		if !exists || len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("required field %q is missing or null", field)
		}
	}
	for _, field := range optional {
		allowed[field] = true
	}
	for field := range object {
		if !allowed[field] {
			return fmt.Errorf("unknown field %q", field)
		}
	}
	return nil
}

func strictDecodeJSONV3(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("has trailing JSON value")
		}
		return fmt.Errorf("has trailing data: %w", err)
	}
	return nil
}

func rejectDuplicateJSONNamesV3(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValueV3(decoder, 0); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("has trailing JSON value")
		}
		return fmt.Errorf("has trailing data: %w", err)
	}
	return nil
}

func walkJSONValueV3(decoder *json.Decoder, depth int) error {
	if depth > 256 {
		return fmt.Errorf("JSON nesting exceeds 256 levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("tokenize JSON: %w", err)
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode object name: %w", err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("object name is not a string")
			}
			if seen[name] {
				return fmt.Errorf("duplicate field %q", name)
			}
			seen[name] = true
			if err := walkJSONValueV3(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValueV3(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}
