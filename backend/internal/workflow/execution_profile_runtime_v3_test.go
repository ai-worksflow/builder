package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type qualifiedReleaseRunnerV3Test struct {
	calls  *atomic.Int32
	result WorkerResult
}

func (r *qualifiedReleaseRunnerV3Test) Run(context.Context, Execution) (WorkerResult, error) {
	if r.calls != nil {
		r.calls.Add(1)
	}
	return r.result, nil
}

func (*qualifiedReleaseRunnerV3Test) workflowQualifiedReleaseControllerDispatchV1() {}

type manifestHookV3Test func(context.Context, Execution) (BuildManifest, error)

func (f manifestHookV3Test) Compile(ctx context.Context, execution Execution) (BuildManifest, error) {
	return f(ctx, execution)
}

func completeRuntimeV3Test() workflowExecutionRuntime {
	runtime := workflowExecutionRuntime{
		runners:            map[domain.WorkflowNodeType]WorkerRunner{},
		manifestCompilers:  map[string]BuildManifestHook{},
		conditionEvaluator: DeclarativeConditionEvaluatorV1{},
		humanContextKeys:   map[string]struct{}{},
	}
	for nodeType, owner := range workflowExecutionProfileV3DispatchOwnership() {
		if owner == workflowNodeDispatchRunner {
			runtime.runners[nodeType] = RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
				return WorkerResult{Disposition: ResultComplete}, nil
			})
		}
	}
	runtime.runners[domain.NodePublish] = &qualifiedReleaseRunnerV3Test{}
	for _, capability := range WorkflowExecutionProfileV3Descriptor().Capabilities.ManifestCompilers {
		runtime.manifestCompilers[manifestCompilerKey(capability.ManifestKind, capability.SchemaVersion, capability.Hook)] = manifestHookV3Test(func(context.Context, Execution) (BuildManifest, error) {
			return BuildManifest{}, errors.New("unexpected manifest compiler execution")
		})
	}
	return runtime
}

func TestWorkflowExecutionProfileV3DispatchOwnershipIsExactAndClosed(t *testing.T) {
	t.Parallel()
	want := map[domain.WorkflowNodeType]workflowNodeDispatchOwner{
		domain.NodeAITransform:               workflowNodeDispatchInterpreter,
		domain.NodeArtifactInput:             workflowNodeDispatchInterpreter,
		domain.NodeCondition:                 workflowNodeDispatchInterpreter,
		domain.NodeExternalQualificationGate: workflowNodeDispatchInterpreter,
		domain.NodeFanOut:                    workflowNodeDispatchRunner,
		domain.NodeHumanEdit:                 workflowNodeDispatchInterpreter,
		domain.NodeManifestCompiler:          workflowNodeDispatchInterpreter,
		domain.NodeMerge:                     workflowNodeDispatchInterpreter,
		domain.NodePublish:                   workflowNodeDispatchRunner,
		domain.NodeQualityGate:               workflowNodeDispatchRunner,
		domain.NodeReviewGate:                workflowNodeDispatchInterpreter,
		domain.NodeTransform:                 workflowNodeDispatchRunner,
		domain.NodeWorkbenchBuild:            workflowNodeDispatchRunner,
	}
	got := workflowExecutionProfileV3DispatchOwnership()
	if len(got) != len(want) {
		t.Fatalf("v3 dispatch owner count = %d, want %d", len(got), len(want))
	}
	for nodeType, owner := range want {
		if got[nodeType] != owner {
			t.Fatalf("v3 owner for %s = %q, want %q", nodeType, got[nodeType], owner)
		}
	}
	registry := NewWorkflowExecutionProfileRegistry()
	if err := registry.Register(workflowExecutionProfileV3Bundle()); err != nil {
		t.Fatalf("exact v3 bundle registration failed: %v", err)
	}

	missing := workflowExecutionProfileV3Bundle()
	delete(missing.nodeDispatch, domain.NodeExternalQualificationGate)
	if err := NewWorkflowExecutionProfileRegistry().Register(missing); err == nil || !strings.Contains(err.Error(), "explicit dispatch owner") {
		t.Fatalf("v3 bundle accepted missing ownership: %v", err)
	}
	extra := workflowExecutionProfileV3Bundle()
	extra.nodeDispatch[domain.WorkflowNodeType("undeclared_v3_node")] = workflowNodeDispatchRunner
	if err := NewWorkflowExecutionProfileRegistry().Register(extra); err == nil || !strings.Contains(err.Error(), "undeclared node type") {
		t.Fatalf("v3 bundle accepted extra ownership: %v", err)
	}
}

func TestHistoricalRunnerDispatchIdentityCannotChangeOwnershipUnderNewVersion(t *testing.T) {
	t.Parallel()
	bundle := currentExecutionProfileBundle()
	bundle.Descriptor.Version = "workflow-engine/future-with-historical-dispatch"
	bundle.componentIdentity = bundle.Descriptor.Components
	bundle.nodeDispatch[domain.NodePublish] = workflowNodeDispatchInterpreter
	err := NewWorkflowExecutionProfileRegistry().Register(bundle)
	if err == nil || !strings.Contains(err.Error(), "changed its frozen dispatch owner") {
		t.Fatalf("historical dispatch identity accepted changed ownership under a new version: %v", err)
	}
}

func TestSealRuntimeV3RequiresQualifiedPublisherAndRejectsExternalRunner(t *testing.T) {
	t.Parallel()
	descriptor := WorkflowExecutionProfileV3Descriptor()

	runtime := completeRuntimeV3Test()
	if _, err := sealRuntimeV3(runtime, descriptor); err != nil {
		t.Fatalf("complete v3 runtime was rejected: %v", err)
	}
	registry := NewWorkflowExecutionProfileRegistry()
	if err := registry.Register(workflowExecutionProfileV3Bundle()); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(runtime); err != nil {
		t.Fatalf("complete v3 registry failed to seal: %v", err)
	}
	if _, err := registry.Resolve(WorkflowExecutionProfileV3Ref()); err != nil {
		t.Fatalf("sealed private v3 bundle did not resolve in its isolated registry: %v", err)
	}

	missing := completeRuntimeV3Test()
	delete(missing.runners, domain.NodePublish)
	if _, err := sealRuntimeV3(missing, descriptor); err == nil || !strings.Contains(err.Error(), "capability is missing") {
		t.Fatalf("v3 runtime accepted a missing qualified publisher: %v", err)
	}
	legacy := completeRuntimeV3Test()
	legacy.runners[domain.NodePublish] = PublishRunner{}
	if _, err := sealRuntimeV3(legacy, descriptor); err == nil || !strings.Contains(err.Error(), "legacy Publish runner") {
		t.Fatalf("v3 runtime accepted the legacy publisher: %v", err)
	}
	forgedGate := completeRuntimeV3Test()
	forgedGate.runners[domain.NodeExternalQualificationGate] = RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{}, nil
	})
	if _, err := sealRuntimeV3(forgedGate, descriptor); err == nil || !strings.Contains(err.Error(), "cannot have a runner") {
		t.Fatalf("v3 runtime accepted an external-gate runner: %v", err)
	}
}

func TestSealProfileCannotBypassRuntimeV3Factory(t *testing.T) {
	t.Parallel()
	registry := NewWorkflowExecutionProfileRegistry()
	bundle := workflowExecutionProfileV3Bundle()
	if err := registry.Register(bundle); err != nil {
		t.Fatal(err)
	}
	legacy := completeRuntimeV3Test()
	legacy.runners[domain.NodePublish] = PublishRunner{}
	if err := registry.SealProfile(WorkflowExecutionProfileV3Ref(), bundle.Descriptor.Components, legacy); err == nil || !strings.Contains(err.Error(), "legacy Publish runner") {
		t.Fatalf("SealProfile bypassed the v3 runtime factory: %v", err)
	}
	if err := registry.SealProfile(WorkflowExecutionProfileV3Ref(), bundle.Descriptor.Components, completeRuntimeV3Test()); err != nil {
		t.Fatalf("SealProfile rejected an exact v3 runtime: %v", err)
	}
	if err := registry.FinishSeal(); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteNodeV3NeverInvokesExternalOrPublishRunnerInPhaseA(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	runtime := completeRuntimeV3Test()
	runtime.runners[domain.NodePublish] = &qualifiedReleaseRunnerV3Test{calls: calls}
	runtime.runners[domain.NodeExternalQualificationGate] = RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		calls.Add(1)
		return WorkerResult{}, nil
	})
	for _, node := range []domain.NodeDefinition{
		{ID: "external-qualification", Type: domain.NodeExternalQualificationGate},
		{ID: "publish", Type: domain.NodePublish},
	} {
		execution := profileRuntimeExecution(WorkflowExecutionProfileV3Ref(), node)
		if _, err := executeNodeV3(context.Background(), engine, runtime, execution); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("v3 %s execute error = %v", node.Type, err)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("Phase A dedicated nodes invoked a runner %d times", got)
	}
}

func TestReconcileV3SkipsExternalGateAndPublishButAdvancesOrdinaryNodes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	definition := domain.WorkflowDefinition{
		Nodes: []domain.NodeDefinition{
			{ID: "quality", Type: domain.NodeQualityGate},
			{ID: "external-qualification", Type: domain.NodeExternalQualificationGate},
			{ID: "publish", Type: domain.NodePublish},
			{ID: "ordinary-source", Type: domain.NodeTransform},
			{ID: "ordinary-target", Type: domain.NodeTransform},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "quality-external", From: "quality", To: "external-qualification"},
			{ID: "external-publish", From: "external-qualification", To: "publish"},
			{ID: "ordinary", From: "ordinary-source", To: "ordinary-target"},
		},
	}
	run := &RunRecord{ID: uuid.NewString(), Context: NewRunContext(), Nodes: map[string]*NodeRecord{}}
	add := func(key string, nodeType domain.WorkflowNodeType, status NodeStatus) {
		run.Nodes[key] = &NodeRecord{ID: uuid.NewString(), RunID: run.ID, Key: key, DefinitionNodeID: key, Type: nodeType, Status: status}
		run.Context.Nodes[key] = NodeMetadata{DefinitionNodeID: key}
	}
	add("quality", domain.NodeQualityGate, NodeCompleted)
	add("external-qualification", domain.NodeExternalQualificationGate, NodePending)
	add("publish", domain.NodePublish, NodePending)
	add("ordinary-source", domain.NodeTransform, NodeCompleted)
	add("ordinary-target", domain.NodeTransform, NodePending)
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	builder := newMutationBuilder(engine, run, now)
	reconcileV3(engine, workflowExecutionRuntime{}, run, definition, builder)
	if run.Nodes["external-qualification"].Status != NodePending || run.Nodes["publish"].Status != NodePending {
		t.Fatalf("v3 reconcile advanced dedicated nodes: gate=%s publish=%s", run.Nodes["external-qualification"].Status, run.Nodes["publish"].Status)
	}
	if run.Nodes["ordinary-target"].Status != NodeReady {
		t.Fatalf("v3 reconcile did not advance ordinary node: %s", run.Nodes["ordinary-target"].Status)
	}
	for _, event := range builder.events {
		if event.NodeKey == "external-qualification" || event.NodeKey == "publish" {
			t.Fatalf("v3 reconcile emitted a dedicated-node event: %+v", event)
		}
	}
}

func TestSharedWorkerClaimAndRenewFenceV3PublishButPreserveHistoricalPublish(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 15, 0, 0, time.UTC)
	v3 := newApplyFixtureV3(t, applyFixtureV3Options{})
	v3.store.mu.Lock()
	v3Stored := v3.store.runs[v3.run.ID]
	v3Stored.Nodes["quality"].Status = NodeCompleted
	v3Stored.Nodes["quality"].LeaseOwner = ""
	v3Stored.Nodes["quality"].LeaseExpiresAt = nil
	v3Stored.Nodes["external-qualification"].Status = NodeCompleted
	v3Stored.Nodes["publish"].Status = NodeReady
	v3Stored.Nodes["publish"].AvailableAt = now.Add(-time.Minute)
	v3.store.mu.Unlock()
	if _, err := v3.store.ClaimRunnable(ctx, "shared-worker", now, time.Minute, WorkflowExecutionProfileV3Ref()); !errors.Is(err, ErrNoRunnableNode) {
		t.Fatalf("shared worker claimed v3 Publish: %v", err)
	}
	v3.store.mu.Lock()
	v3Publish := v3.store.runs[v3.run.ID].Nodes["publish"]
	v3Publish.Status, v3Publish.Attempt, v3Publish.LeaseOwner = NodeRunning, 1, "forged-shared-worker"
	v3Expiry := now.Add(time.Minute)
	v3Publish.LeaseExpiresAt = &v3Expiry
	v3.store.mu.Unlock()
	if _, err := v3.store.RenewLease(ctx, Lease{
		RunID: v3.run.ID, NodeID: v3Publish.ID, NodeKey: v3Publish.Key, WorkerID: "forged-shared-worker", Attempt: 1, LeaseExpiresAt: v3Expiry,
	}, now, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("shared worker renewed forged v3 Publish lease: %v", err)
	}

	seededDefinition, err := MinimumLoopDefinition(uuid.NewString(), uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		uuid.NewString(), 1, seededDefinition.Name, seededDefinition.SchemaVersion,
		seededDefinition.Nodes, seededDefinition.Edges, *seededDefinition.InputContract, *seededDefinition.OutputContract,
		CurrentWorkflowExecutionProfileRef(), uuid.NewString(), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	projectID, versionID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := v3.store.SaveDefinition(ctx, DefinitionRecord{
		VersionID: versionID, ProjectID: projectID, Key: "historical-publish", Title: definition.Name,
		Published: true, ExecutionProfile: CurrentWorkflowExecutionProfileRef(), Definition: definition,
	}); err != nil {
		t.Fatal(err)
	}
	contextV2 := NewRunContext()
	contextV2.Nodes["publish"] = NodeMetadata{DefinitionNodeID: "publish", MaxAttempts: 3}
	publishV2 := &NodeRecord{
		ID: uuid.NewString(), RunID: runID, Key: "publish", DefinitionNodeID: "publish", Type: domain.NodePublish,
		Status: NodeReady, AvailableAt: now.Add(-time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	runV2 := &RunRecord{
		ID: runID, ProjectID: projectID, DefinitionVersionID: versionID,
		Definition: definition.RefForExecutionProfile(CurrentWorkflowExecutionProfileRef()), ExecutionProfile: CurrentWorkflowExecutionProfileRef(),
		Status: RunRunning, GovernanceMode: core.GovernanceModeSolo, Scope: json.RawMessage(`{}`), Context: contextV2,
		StartedBy: uuid.NewString(), StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*NodeRecord{"publish": publishV2},
	}
	if err := v3.store.CreateRun(ctx, runV2, []Event{{ID: uuid.NewString(), RunID: runID, Type: "run.started", Payload: json.RawMessage(`{}`), CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	first, err := v3.store.ClaimRunnable(ctx, "historical-worker", now, time.Second, CurrentWorkflowExecutionProfileRef())
	if err != nil || first.NodeID != publishV2.ID || first.Attempt != 1 {
		t.Fatalf("historical Publish was not claimable: lease=%+v err=%v", first, err)
	}
	secondNow := now.Add(2 * time.Second)
	second, err := v3.store.ClaimRunnable(ctx, "historical-worker", secondNow, time.Minute, CurrentWorkflowExecutionProfileRef())
	if err != nil || second.Attempt != 2 {
		t.Fatalf("historical Publish lease was not reclaimed: lease=%+v err=%v", second, err)
	}
	if _, err := v3.store.RenewLease(ctx, first, secondNow.Add(time.Second), time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old same-worker attempt renewed the new lease epoch: %v", err)
	}
	if _, err := v3.store.RenewLease(ctx, second, secondNow.Add(time.Second), time.Minute); err != nil {
		t.Fatalf("current historical Publish attempt could not renew: %v", err)
	}
}

func TestSharedWorkerSQLFencesV3PublishAndBindsRenewalAttempt(t *testing.T) {
	normalizedClaim := strings.ToLower(claimRunnableSQL)
	for _, fragment := range []string{
		"node.node_type <> 'external_qualification_gate'",
		"run.execution_profile_version = 'workflow-engine/v3'",
		"run.execution_profile_hash = '" + WorkflowExecutionProfileV3Hash + "'",
		"node.node_type = 'publish'",
	} {
		if !strings.Contains(normalizedClaim, fragment) {
			t.Fatalf("claim SQL omits v3 dedicated-node fence %q", fragment)
		}
	}
	normalizedRenew := strings.ToLower(renewLeaseWhereSQL)
	for _, fragment := range []string{
		"node_type <> ?", "attempt = ?", "lease_owner = ?", "lease_expires_at >= ?",
		"node_type = ? and exists", "run.execution_profile_version = 'workflow-engine/v3'",
		"run.execution_profile_hash = '" + WorkflowExecutionProfileV3Hash + "'",
	} {
		if !strings.Contains(strings.Join(strings.Fields(normalizedRenew), " "), fragment) {
			t.Fatalf("renew SQL omits lease/v3 fence %q: %s", fragment, normalizedRenew)
		}
	}
}

type fixedClockV3Test struct{ value time.Time }

func (c fixedClockV3Test) Now() time.Time { return c.value }

type applyFixtureV3Options struct {
	gateInput             json.RawMessage
	qualityOutputRevision string
	findingsPadding       int
}

type applyFixtureV3 struct {
	store      *MemoryStore
	engine     *Engine
	definition domain.WorkflowDefinition
	run        *RunRecord
	node       *NodeRecord
	lease      Lease
	execution  Execution
	result     WorkerResult
	workspace  domain.ArtifactRef
}

func newApplyFixtureV3(t *testing.T, options applyFixtureV3Options) applyFixtureV3 {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 30, 0, 123456789, time.UTC)
	definition := profileV3RuntimeFenceDefinition(t, now)
	projectID, runID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	if err := store.SaveDefinition(ctx, DefinitionRecord{
		VersionID: versionID, ProjectID: projectID, Key: "v3-atomic-quality", Title: definition.Name,
		Published: true, ExecutionProfile: WorkflowExecutionProfileV3Ref(), Definition: definition,
	}); err != nil {
		t.Fatal(err)
	}
	qualityDefinition, _ := definition.FindNode("quality")
	leaseExpiry := now.Add(time.Minute)
	quality := &NodeRecord{
		ID: uuid.NewString(), RunID: runID, Key: "quality", DefinitionNodeID: "quality", Type: domain.NodeQualityGate,
		Status: NodeRunning, Attempt: 1, OutputRevisionID: options.qualityOutputRevision,
		LeaseOwner: "quality-worker", LeaseExpiresAt: &leaseExpiry, AvailableAt: now, StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now,
	}
	gate := &NodeRecord{
		ID: uuid.NewString(), RunID: runID, Key: "external-qualification", DefinitionNodeID: "external-qualification",
		Type: domain.NodeExternalQualificationGate, Status: NodePending, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}
	publish := &NodeRecord{
		ID: uuid.NewString(), RunID: runID, Key: "publish", DefinitionNodeID: "publish",
		Type: domain.NodePublish, Status: NodePending, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}
	runContext := NewRunContext()
	runContext.Nodes[quality.Key] = NodeMetadata{DefinitionNodeID: quality.DefinitionNodeID, MaxAttempts: 1, TimeoutNanos: int64(time.Minute)}
	runContext.Nodes[gate.Key] = NodeMetadata{DefinitionNodeID: gate.DefinitionNodeID, MaxAttempts: 1, Input: options.gateInput}
	runContext.Nodes[publish.Key] = NodeMetadata{DefinitionNodeID: publish.DefinitionNodeID, MaxAttempts: 1}
	run := &RunRecord{
		ID: runID, ProjectID: projectID, DefinitionVersionID: versionID,
		Definition: definition.RefForExecutionProfile(WorkflowExecutionProfileV3Ref()), ExecutionProfile: WorkflowExecutionProfileV3Ref(),
		Status: RunRunning, GovernanceMode: core.GovernanceModeSolo, Scope: json.RawMessage(`{}`), Context: runContext,
		StartedBy: uuid.NewString(), StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*NodeRecord{quality.Key: quality, gate.Key: gate, publish.Key: publish},
	}
	if err := store.CreateRun(ctx, run, []Event{{ID: uuid.NewString(), RunID: runID, Type: "run.started", Payload: json.RawMessage(`{}`), CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	engine.Clock = fixedClockV3Test{value: now}
	inputs, err := domain.NewNodeInputEnvelope(nil)
	if err != nil {
		t.Fatal(err)
	}
	qualityOutput, workspace := qualityResultOutputV3Test(t, projectID, runID, now, options.findingsPadding)
	lease := Lease{RunID: runID, NodeID: quality.ID, NodeKey: quality.Key, WorkerID: quality.LeaseOwner, Attempt: quality.Attempt, LeaseExpiresAt: leaseExpiry}
	node := stored.Nodes[quality.Key]
	execution := Execution{Run: *stored, Node: *node, Definition: qualityDefinition, Workflow: definition, Lease: lease, Inputs: inputs}
	return applyFixtureV3{
		store: store, engine: engine, definition: definition, run: stored, node: node, lease: lease, execution: execution,
		result: WorkerResult{Disposition: ResultComplete, Output: qualityOutput}, workspace: workspace,
	}
}

func qualityResultOutputV3Test(t *testing.T, projectID, runID string, now time.Time, padding int) (json.RawMessage, domain.ArtifactRef) {
	t.Helper()
	workspace := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: strings.Repeat("a", 64)}
	manifest := BuildManifest{
		SchemaVersion: 1, ProjectID: projectID, RunID: runID, ManifestGroupKey: uuid.NewString(),
		SliceIDs: []string{uuid.NewString()}, BundleIDs: []string{uuid.NewString()},
		Sources:     []domain.ArtifactRef{{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: strings.Repeat("b", 64)}},
		Constraints: json.RawMessage(`{}`), CreatedAt: now,
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	checks := []any{}
	if padding > 0 {
		checks = append(checks, strings.Repeat("x", padding))
	}
	qualityRunID := uuid.NewString()
	findings, err := domain.CanonicalJSON(map[string]any{
		"checks": checks, "diagnostics": []any{}, "qualityRunId": qualityRunID,
		"reportArtifactId": uuid.NewString(), "reportRevisionId": uuid.NewString(), "score": 100,
		"workspaceRevision": map[string]any{"artifactId": workspace.ArtifactID, "revisionId": workspace.RevisionID, "contentHash": workspace.ContentHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mustJSON(QualityResult{
		Passed: true, Findings: findings, QualityRunID: qualityRunID, WorkspaceRevision: &workspace, BuildManifest: &manifest,
	}), workspace
}

func TestApplyResultV3CommitsQualityAndCanonicalGateInputAtomically(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	if err := applyResultV3(context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run, fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result); err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.store.GetRun(context.Background(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	quality, gate, publish := stored.Nodes["quality"], stored.Nodes["external-qualification"], stored.Nodes["publish"]
	if quality.Status != NodeCompleted || quality.OutputRevisionID != fixture.workspace.RevisionID || gate.Status != NodePending || gate.Attempt != 0 || publish.Status != NodePending {
		t.Fatalf("v3 Quality closure = quality(%s,%s) gate(%s,%d) publish(%s)", quality.Status, quality.OutputRevisionID, gate.Status, gate.Attempt, publish.Status)
	}
	gateRaw := stored.Context.Nodes[gate.Key].Input
	if len(gateRaw) == 0 || len(gateRaw) > maximumQualityResultV3Bytes {
		t.Fatalf("stored gate input size = %d", len(gateRaw))
	}
	var envelope domain.NodeInputEnvelope
	if err := json.Unmarshal(gateRaw, &envelope); err != nil {
		t.Fatalf("decode stored gate input: %v", err)
	}
	bindings := envelope.Bindings()
	if len(bindings) != 1 || !bytes.Equal(bindings[0].Output, bindings[0].Value) ||
		bindings[0].Source.OutputRevisionID != fixture.workspace.RevisionID || bindings[0].Source.NodeKey != "quality" {
		t.Fatalf("stored gate binding is not the exact Quality projection: %+v", bindings)
	}
	foundWorkspace := false
	for _, ref := range bindings[0].Source.ArtifactRevisions {
		foundWorkspace = foundWorkspace || ref.Equal(fixture.workspace)
	}
	if !foundWorkspace {
		t.Fatal("stored gate input omits the Quality WorkspaceRevision")
	}
	events, err := fixture.store.ListEvents(context.Background(), stored.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Type != "node.completed" || events[1].NodeKey != "quality" || stored.EventCursor != 2 {
		t.Fatalf("atomic Quality events/cursor = %+v / %d", events, stored.EventCursor)
	}
	if stored.Status != RunRunning {
		t.Fatalf("pending external qualification must keep run running, got %s", stored.Status)
	}
}

func assertApplyV3DidNotCommit(t *testing.T, fixture applyFixtureV3) {
	t.Helper()
	stored, err := fixture.store.GetRun(context.Background(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EventCursor != 1 || stored.Nodes["quality"].Status != NodeRunning || stored.Nodes["external-qualification"].Status != NodePending {
		t.Fatalf("failed v3 apply committed partial state: cursor=%d quality=%s gate=%s", stored.EventCursor, stored.Nodes["quality"].Status, stored.Nodes["external-qualification"].Status)
	}
}

func TestApplyResultV3RejectsPrestoredGateInputWithoutCommit(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{gateInput: json.RawMessage(`{}`)})
	err := applyResultV3(context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run, fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result)
	if err == nil || !strings.Contains(err.Error(), "context is not pristine") {
		t.Fatalf("pre-stored gate input error = %v", err)
	}
	assertApplyV3DidNotCommit(t, fixture)
}

func TestApplyResultV3RejectsNonPristineGateInterpreterStateWithoutCommit(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*NodeMetadata)
	}{
		{name: "selected branch", mutate: func(metadata *NodeMetadata) { metadata.SelectedBranch = "forged" }},
		{name: "fan-out outputs", mutate: func(metadata *NodeMetadata) {
			metadata.FanOutOutputs = map[string]json.RawMessage{"forged": json.RawMessage(`{}`)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
			metadata := fixture.run.Context.Nodes["external-qualification"]
			test.mutate(&metadata)
			fixture.run.Context.Nodes["external-qualification"] = metadata
			err := applyResultV3(context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run, fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result)
			if err == nil || !strings.Contains(err.Error(), "context is not pristine") {
				t.Fatalf("forged gate interpreter state error = %v", err)
			}
			assertApplyV3DidNotCommit(t, fixture)
		})
	}
}

func TestApplyResultV3RejectsForgedMissingOrMismatchedWorkspaceRevisionWithoutCommit(t *testing.T) {
	tests := []struct {
		name    string
		options applyFixtureV3Options
		mutate  func(t *testing.T, fixture *applyFixtureV3)
	}{
		{name: "forged preexisting node output revision", options: applyFixtureV3Options{qualityOutputRevision: uuid.NewString()}},
		{name: "missing result revision", mutate: func(t *testing.T, fixture *applyFixtureV3) {
			var result QualityResult
			if err := json.Unmarshal(fixture.result.Output, &result); err != nil {
				t.Fatal(err)
			}
			result.WorkspaceRevision.RevisionID = ""
			fixture.result.Output = mustJSON(result)
		}},
		{name: "findings revision mismatch", mutate: func(t *testing.T, fixture *applyFixtureV3) {
			var result QualityResult
			if err := json.Unmarshal(fixture.result.Output, &result); err != nil {
				t.Fatal(err)
			}
			var findings map[string]any
			if err := json.Unmarshal(result.Findings, &findings); err != nil {
				t.Fatal(err)
			}
			findings["workspaceRevision"].(map[string]any)["revisionId"] = uuid.NewString()
			result.Findings = mustJSON(findings)
			fixture.result.Output = mustJSON(result)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyFixtureV3(t, test.options)
			if test.mutate != nil {
				test.mutate(t, &fixture)
			}
			err := applyResultV3(context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run, fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result)
			if err == nil {
				t.Fatal("invalid Workspace revision was accepted")
			}
			assertApplyV3DidNotCommit(t, fixture)
		})
	}
}

type failingCommitStoreV3 struct {
	Store
	commits atomic.Int32
	err     error
}

func (s *failingCommitStoreV3) Commit(context.Context, RunMutation) error {
	s.commits.Add(1)
	return s.err
}

func TestApplyResultV3StoreFailureLeavesQualityAndGateUnchanged(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	failure := errors.New("injected atomic commit failure")
	store := &failingCommitStoreV3{Store: fixture.store, err: failure}
	fixture.engine.Store = store
	err := applyResultV3(context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run, fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result)
	if !errors.Is(err, failure) || store.commits.Load() != 1 {
		t.Fatalf("commit failure/count = %v / %d", err, store.commits.Load())
	}
	assertApplyV3DidNotCommit(t, fixture)
}

func TestApplyResultV3ConcurrentQualityCompletionHasOneAtomicWinner(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	left, err := fixture.store.GetRun(context.Background(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	right, err := fixture.store.GetRun(context.Background(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	qualityDefinition, _ := fixture.definition.FindNode("quality")
	inputs, _ := domain.NewNodeInputEnvelope(nil)
	apply := func(run *RunRecord) error {
		node := run.Nodes["quality"]
		execution := Execution{Run: *run, Node: *node, Definition: qualityDefinition, Workflow: fixture.definition, Lease: fixture.lease, Inputs: inputs}
		return applyResultV3(context.Background(), fixture.engine, workflowExecutionRuntime{}, run, fixture.definition, node, fixture.lease, execution, fixture.result)
	}
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for _, run := range []*RunRecord{left, right} {
		wait.Add(1)
		go func(run *RunRecord) {
			defer wait.Done()
			<-start
			errorsFound <- apply(run)
		}(run)
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	successes, conflicts := 0, 0
	for err := range errorsFound {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCASConflict):
			conflicts++
		default:
			t.Fatalf("concurrent v3 apply returned %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent v3 apply successes/conflicts = %d/%d", successes, conflicts)
	}
	stored, _ := fixture.store.GetRun(context.Background(), fixture.run.ID)
	if stored.EventCursor != 2 || stored.Nodes["quality"].Status != NodeCompleted || len(stored.Context.Nodes["external-qualification"].Input) == 0 {
		t.Fatalf("concurrent v3 winner did not produce one complete closure: %+v", stored)
	}
}

func TestDecodeQualityResultV3MatchesWIAExactWire(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	base := fixture.result.Output
	if _, err := decodeQualityResultV3(base, fixture.run); err != nil {
		t.Fatalf("valid QualityResult was rejected: %v", err)
	}
	mutate := func(t *testing.T, change func(map[string]any)) json.RawMessage {
		t.Helper()
		var object map[string]any
		if err := json.Unmarshal(base, &object); err != nil {
			t.Fatal(err)
		}
		change(object)
		return mustJSON(object)
	}
	tests := map[string]json.RawMessage{
		"unknown top-level":     mutate(t, func(object map[string]any) { object["extra"] = true }),
		"missing findings":      mutate(t, func(object map[string]any) { delete(object, "findings") }),
		"workspace anchor null": mutate(t, func(object map[string]any) { object["workspaceRevision"].(map[string]any)["anchorId"] = nil }),
		"findings workspace anchor null": mutate(t, func(object map[string]any) {
			object["findings"].(map[string]any)["workspaceRevision"].(map[string]any)["anchorId"] = nil
		}),
		"source anchor null": mutate(t, func(object map[string]any) {
			object["buildManifest"].(map[string]any)["sources"].([]any)[0].(map[string]any)["anchorId"] = nil
		}),
		"source anchor empty": mutate(t, func(object map[string]any) {
			object["buildManifest"].(map[string]any)["sources"].([]any)[0].(map[string]any)["anchorId"] = ""
		}),
		"noncanonical UTC time": mutate(t, func(object map[string]any) {
			created := object["buildManifest"].(map[string]any)["createdAt"].(string)
			object["buildManifest"].(map[string]any)["createdAt"] = strings.TrimSuffix(created, "Z") + "+00:00"
		}),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeQualityResultV3(raw, fixture.run); err == nil {
				t.Fatal("invalid WIA QualityResult wire was accepted")
			}
		})
	}
	duplicate := append([]byte(`{"passed":true,"passed":true,`), bytes.TrimPrefix(base, []byte(`{"passed":true,`))...)
	if _, err := decodeQualityResultV3(duplicate, fixture.run); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate QualityResult field error = %v", err)
	}
}

func TestDecodeQualityResultV3RejectsWIAUnsafeManifestBounds(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	decode := func(t *testing.T) QualityResult {
		t.Helper()
		var result QualityResult
		if err := json.Unmarshal(fixture.result.Output, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	tests := []struct {
		name   string
		mutate func(*BuildManifest)
	}{
		{name: "schema above JavaScript safe integer", mutate: func(manifest *BuildManifest) { manifest.SchemaVersion = int(maximumJavaScriptSafeIntegerV3 + 1) }},
		{name: "more than 1024 slices", mutate: func(manifest *BuildManifest) {
			manifest.SliceIDs, manifest.BundleIDs = make([]string, maximumQualityManifestSlicesV3+1), make([]string, maximumQualityManifestSlicesV3+1)
			for index := range manifest.SliceIDs {
				manifest.SliceIDs[index], manifest.BundleIDs[index] = uuid.NewString(), uuid.NewString()
			}
		}},
		{name: "more than 2048 sources", mutate: func(manifest *BuildManifest) {
			manifest.Sources = make([]domain.ArtifactRef, maximumQualityManifestSourcesV3+1)
			for index := range manifest.Sources {
				manifest.Sources[index] = domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: strings.Repeat("c", 64)}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := decode(t)
			test.mutate(result.BuildManifest)
			result.BuildManifest.Hash = ""
			if err := result.BuildManifest.Freeze(); err != nil {
				t.Fatal(err)
			}
			if _, err := decodeQualityResultV3(mustJSON(result), fixture.run); err == nil {
				t.Fatal("WIA-unsafe BuildManifest bound was accepted")
			}
		})
	}
}

func TestApplyResultV3RejectsOversizedCanonicalGateEnvelopeWithoutCommit(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{findingsPadding: 8 << 20})
	if len(fixture.result.Output) >= maximumQualityResultV3Bytes {
		t.Fatalf("fixture QualityResult must fit WIA input limit, got %d", len(fixture.result.Output))
	}
	err := applyResultV3(context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run, fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result)
	if err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("oversized gate envelope error = %v", err)
	}
	assertApplyV3DidNotCommit(t, fixture)
}
