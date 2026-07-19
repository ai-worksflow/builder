package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

type executionProfileCaptureFreezer struct{ seen []Execution }

func (f *executionProfileCaptureFreezer) Freeze(_ context.Context, execution Execution) (domain.InputManifest, error) {
	f.seen = append(f.seen, execution)
	return domain.InputManifest{}, nil
}

func TestWorkflowExecutionProfileDescriptorsHaveGoldenRefs(t *testing.T) {
	t.Parallel()
	currentDescriptor := CurrentWorkflowExecutionProfileDescriptor()
	v2Descriptor := WorkflowExecutionProfileV2Descriptor()
	v1Descriptor := WorkflowExecutionProfileV1Descriptor()
	current, err := currentDescriptor.Ref()
	if err != nil {
		t.Fatal(err)
	}
	v2, err := v2Descriptor.Ref()
	if err != nil {
		t.Fatal(err)
	}
	v1, err := v1Descriptor.Ref()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := LegacyWorkflowExecutionProfileDescriptor().Ref()
	if err != nil {
		t.Fatal(err)
	}
	if v2 != (domain.WorkflowExecutionProfileRef{Version: WorkflowExecutionProfileV2Version, Hash: WorkflowExecutionProfileV2Hash}) {
		t.Fatalf("workflow-engine/v2 descriptor drifted from its frozen snapshot: %+v", v2)
	}
	if current != v2 || CurrentWorkflowExecutionProfileRef() != WorkflowExecutionProfileV2Ref() {
		t.Fatalf("current execution profile is not the exact workflow-engine/v2 alias: current=%+v v2=%+v", current, v2)
	}
	if v1 != (domain.WorkflowExecutionProfileRef{Version: WorkflowExecutionProfileV1Version, Hash: WorkflowExecutionProfileV1Hash}) {
		t.Fatalf("workflow-engine/v1 descriptor drifted from its frozen snapshot: %+v", v1)
	}
	if legacy != (domain.WorkflowExecutionProfileRef{Version: LegacyWorkflowExecutionProfileVersion, Hash: LegacyWorkflowExecutionProfileHash}) {
		t.Fatalf("legacy execution profile descriptor drifted from the migration snapshot: %+v", legacy)
	}
	if legacy.Version == v1.Version || legacy.Version == v2.Version || v1.Version == v2.Version ||
		legacy.Hash == v1.Hash || legacy.Hash == v2.Hash || v1.Hash == v2.Hash ||
		legacyWorkflowCapabilitiesSnapshot().Version != 3 || workflowCapabilitiesV1Snapshot().Version != 4 || workflowCapabilitiesV2Snapshot().Version != 4 || currentProfileCapabilitiesVersion() != 4 {
		t.Fatalf("legacy, v1 and v2 execution profiles are not independent: legacy=%+v v1=%+v v2=%+v", legacy, v1, v2)
	}
	v1CapabilitiesHash, err := domain.CanonicalHash(v1Descriptor.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	v2CapabilitiesHash, err := domain.CanonicalHash(v2Descriptor.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	v1Components := v1Descriptor.Components
	if v1Components.ReconcileID == v2Descriptor.Components.ReconcileID {
		t.Fatal("workflow-engine/v2 did not pin a distinct reconciliation component")
	}
	v1Components.ReconcileID = v2Descriptor.Components.ReconcileID
	if v1CapabilitiesHash != v2CapabilitiesHash || v1Components != v2Descriptor.Components {
		t.Fatalf("workflow-engine/v2 changed more than its reconciliation component: v1=%+v v2=%+v", v1Descriptor, v2Descriptor)
	}

	mutations := []func(*WorkflowExecutionProfileDescriptor){
		func(d *WorkflowExecutionProfileDescriptor) { d.Capabilities.AnalysisLimits.MaxSemanticPathStates++ },
		func(d *WorkflowExecutionProfileDescriptor) { d.Capabilities.FanOutMaximumItems["blueprint_page"]-- },
		func(d *WorkflowExecutionProfileDescriptor) {
			d.Components.ResultValidatorID = "typed-result-validator/v2"
		},
	}
	for index, mutate := range mutations {
		descriptor := WorkflowExecutionProfileV2Descriptor()
		// Clone the nested map before mutation so this test cannot alter a shared
		// capability snapshot if the implementation changes later.
		limits := make(map[string]int, len(descriptor.Capabilities.FanOutMaximumItems))
		for key, value := range descriptor.Capabilities.FanOutMaximumItems {
			limits[key] = value
		}
		descriptor.Capabilities.FanOutMaximumItems = limits
		mutate(&descriptor)
		ref, err := descriptor.Ref()
		if err != nil {
			t.Fatal(err)
		}
		if ref.Hash == v2.Hash {
			t.Fatalf("descriptor mutation %d did not change the execution profile hash", index)
		}
	}
}

func TestWorkflowExecutionProfileV2SnapshotIsIndependentFromAuthoringFactory(t *testing.T) {
	t.Parallel()
	frozen := WorkflowExecutionProfileV2Descriptor()
	frozenCapabilitiesHash, err := domain.CanonicalHash(frozen.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	authoring := PlatformWorkflowCapabilities(true, true)
	authoring.Version++
	authoring.FanOutMaximumItems["blueprint_page"]--
	authoring.ManifestCompilers[0].AllowedContextArtifactKinds = append(
		authoring.ManifestCompilers[0].AllowedContextArtifactKinds,
		"future_contract",
	)
	authoring.InputContracts[0].ManifestSchemaContracts["workflow_start"] = "workflow-input/future"
	driftedAuthoringHash, err := domain.CanonicalHash(authoring)
	if err != nil {
		t.Fatal(err)
	}
	if driftedAuthoringHash == frozenCapabilitiesHash {
		t.Fatal("mutated authoring factory value did not diverge from the frozen v2 snapshot")
	}
	after := WorkflowExecutionProfileV2Descriptor()
	afterCapabilitiesHash, err := domain.CanonicalHash(after.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if afterCapabilitiesHash != frozenCapabilitiesHash || WorkflowExecutionProfileV2Ref() != (domain.WorkflowExecutionProfileRef{Version: WorkflowExecutionProfileV2Version, Hash: WorkflowExecutionProfileV2Hash}) {
		t.Fatalf("authoring capability mutation affected workflow-engine/v2: before=%s after=%s", frozenCapabilitiesHash, afterCapabilitiesHash)
	}
}

func TestProductionSealRejectsAuthoringCapabilitiesThatDriftFromFrozenV2(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	engine.Capabilities = PlatformWorkflowCapabilities(true, true)
	engine.Capabilities.Version++
	if err := engine.SealProductionExecutionProfiles(); err == nil || !strings.Contains(err.Error(), "capabilities do not match") {
		t.Fatalf("production seal accepted authoring capability drift: %v", err)
	}
}

func currentProfileCapabilitiesVersion() int {
	return CurrentWorkflowExecutionProfileDescriptor().Capabilities.Version
}

func TestExecutionProfileRegistryRequiresExactCompleteBundle(t *testing.T) {
	t.Parallel()
	registry, err := NewBuiltinWorkflowExecutionProfileRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.SupportedRefs()) != 3 {
		t.Fatalf("built-in profile registry = %+v", registry.SupportedRefs())
	}
	wantRefs := map[domain.WorkflowExecutionProfileRef]bool{
		LegacyWorkflowExecutionProfileRef(): false,
		WorkflowExecutionProfileV1Ref():     false,
		WorkflowExecutionProfileV2Ref():     false,
	}
	for _, ref := range registry.SupportedRefs() {
		if _, expected := wantRefs[ref]; !expected {
			t.Fatalf("built-in profile registry contains unexpected ref %+v", ref)
		}
		wantRefs[ref] = true
	}
	for ref, present := range wantRefs {
		if !present {
			t.Fatalf("built-in profile registry is missing exact ref %+v", ref)
		}
	}
	wrong := CurrentWorkflowExecutionProfileRef()
	wrong.Hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := registry.Resolve(wrong); err == nil {
		t.Fatal("same-version profile with a different descriptor hash fell back to current")
	}
	if err := NewWorkflowExecutionProfileRegistry().Register(WorkflowExecutionProfileBundle{Descriptor: WorkflowExecutionProfileV2Descriptor()}); err == nil {
		t.Fatal("incomplete execution profile bundle was registered")
	}
	drifted := workflowExecutionProfileV2Bundle()
	drifted.componentIdentity.RunnerDispatchID = "different-runtime"
	if err := NewWorkflowExecutionProfileRegistry().Register(drifted); err == nil {
		t.Fatal("runtime component identity different from the hashed descriptor was registered")
	}
}

func TestLegacyAndCurrentBundlesExposeIndependentRunnerExecutionViews(t *testing.T) {
	t.Parallel()
	registry := NewMapRegistry()
	seen := make([]Execution, 0, 2)
	if err := registry.Register(domain.NodeTransform, RunnerFunc(func(_ context.Context, execution Execution) (WorkerResult, error) {
		seen = append(seen, execution)
		return WorkerResult{Disposition: ResultComplete}, nil
	})); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	engine.Runners = registry
	node := domain.NodeDefinition{ID: "transform", Name: "Transform", Type: domain.NodeTransform, Transform: &domain.TransformNodeConfig{Transform: "selection_passthrough"}}
	current := CurrentWorkflowExecutionProfileRef()
	execution := Execution{
		Run:  RunRecord{ID: uuid.NewString(), Definition: domain.WorkflowDefinitionRef{ID: uuid.NewString(), Version: 1, Hash: strings.Repeat("a", 64), ExecutionProfile: current}, ExecutionProfile: current, Context: NewRunContext()},
		Node: NodeRecord{Key: "transform", DefinitionNodeID: "transform", Type: domain.NodeTransform}, Definition: node,
		Workflow: domain.WorkflowDefinition{ExecutionProfile: current},
	}
	if _, err := currentExecutionProfileBundle().executeNode(context.Background(), engine, execution); err != nil {
		t.Fatal(err)
	}
	legacy := LegacyWorkflowExecutionProfileRef()
	execution.Run.ExecutionProfile = legacy
	execution.Run.Definition.ExecutionProfile = legacy
	execution.Workflow.ExecutionProfile = legacy
	if _, err := legacyExecutionProfileBundle().executeNode(context.Background(), engine, execution); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].Run.ExecutionProfile != current || seen[0].Run.Definition.ExecutionProfile != current || seen[0].Workflow.ExecutionProfile != current || seen[0].legacyProfileView {
		t.Fatalf("current runner did not receive exact profile pins: %+v", seen)
	}
	if !seen[1].Run.ExecutionProfile.IsZero() || !seen[1].Run.Definition.ExecutionProfile.IsZero() || !seen[1].Workflow.ExecutionProfile.IsZero() || !seen[1].legacyProfileView {
		t.Fatalf("legacy runner observed post-pin fields: %+v", seen[1])
	}

	freezer := &executionProfileCaptureFreezer{}
	engine.ManifestFreezer = freezer
	execution.Definition = domain.NodeDefinition{ID: "ai", Name: "AI", Type: domain.NodeAITransform, AITransform: &domain.AITransformNodeConfig{JobType: "test", OutputSchemaVersion: "v1"}}
	execution.Node = NodeRecord{Key: "ai", DefinitionNodeID: "ai", Type: domain.NodeAITransform}
	execution.Run.ExecutionProfile = current
	execution.Run.Definition.ExecutionProfile = current
	execution.Workflow.ExecutionProfile = current
	if _, err := currentExecutionProfileBundle().executeNode(context.Background(), engine, execution); err != nil {
		t.Fatal(err)
	}
	execution.Run.ExecutionProfile = legacy
	execution.Run.Definition.ExecutionProfile = legacy
	execution.Workflow.ExecutionProfile = legacy
	if _, err := legacyExecutionProfileBundle().executeNode(context.Background(), engine, execution); err != nil {
		t.Fatal(err)
	}
	if len(freezer.seen) != 2 || freezer.seen[0].Run.ExecutionProfile != current || !freezer.seen[1].Run.ExecutionProfile.IsZero() || !freezer.seen[1].Run.Definition.ExecutionProfile.IsZero() {
		t.Fatalf("legacy/current manifest freezer views are not independently versioned: %+v", freezer.seen)
	}
}

func TestClaimRunnableFiltersUnsupportedProfileBeforeAttempt(t *testing.T) {
	definition := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	engine, store, _, record, manifest, projectID, actorID := newTestEngine(t, definition, nil)
	run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: actorID})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Readiness(context.Background()); err != nil {
		t.Fatalf("current profile was not ready: %v", err)
	}
	events, err := store.ListEvents(context.Background(), run.ID, 0, 10)
	if err != nil || len(events) == 0 {
		t.Fatalf("load run.started audit event: events=%+v err=%v", events, err)
	}
	var started struct {
		ExecutionProfile domain.WorkflowExecutionProfileRef `json:"executionProfile"`
	}
	if err := json.Unmarshal(events[0].Payload, &started); err != nil || started.ExecutionProfile != CurrentWorkflowExecutionProfileRef() {
		t.Fatalf("run.started event omitted exact execution profile: payload=%s err=%v", events[0].Payload, err)
	}
	legacyOnly := NewWorkflowExecutionProfileRegistry()
	if err := legacyOnly.Register(legacyExecutionProfileBundle()); err != nil {
		t.Fatal(err)
	}
	engine.ExecutionProfiles = legacyOnly
	if err := engine.Readiness(context.Background()); err == nil {
		t.Fatal("unsupported active execution profile was not reported by readiness")
	}
	if err := engine.ClaimAndExecute(context.Background(), "legacy-only-worker"); !errors.Is(err, ErrNoRunnableNode) {
		t.Fatalf("unsupported worker claim = %v", err)
	}
	stored, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Nodes["source"].Attempt != 0 || stored.Nodes["source"].Status != NodeReady {
		t.Fatalf("unsupported profile claim consumed work: %+v", stored.Nodes["source"])
	}
}

func TestProductionLegacyDefinitionCannotStartButPinnedLegacyRunRemainsResolvable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	definition := simpleDefinition(t, uuid.NewString(), uuid.NewString(), time.Now().UTC())
	store := NewMemoryStore(nil)
	record := DefinitionRecord{VersionID: uuid.NewString(), ProjectID: uuid.NewString(), Key: "legacy", Title: "Legacy", Published: true, Definition: definition}
	if err := store.SaveDefinition(ctx, record); err != nil {
		t.Fatal(err)
	}
	record, _ = store.GetDefinitionVersion(ctx, record.VersionID)
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	if _, err := engine.executionProfile(record.ExecutionProfile); err != nil {
		t.Fatalf("registered legacy replay bundle unavailable: %v", err)
	}
	engine.RequireGovernedStarts = true
	_, err = engine.Start(ctx, StartRequest{ProjectID: record.ProjectID, DefinitionVersionID: record.VersionID, StartedBy: uuid.NewString(), InputManifest: domain.ManifestRef{ID: uuid.NewString(), Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
	if err == nil {
		t.Fatal("legacy pre-pin definition started a new run")
	}
}
