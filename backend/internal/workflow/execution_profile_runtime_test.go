package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

type profileBuildManifestHookFunc func(context.Context, Execution) (BuildManifest, error)

func (f profileBuildManifestHookFunc) Compile(ctx context.Context, execution Execution) (BuildManifest, error) {
	return f(ctx, execution)
}

type profileWorkbenchCompletionFunc func(context.Context, Execution, json.RawMessage) (string, error)

func (f profileWorkbenchCompletionFunc) ValidateCompletion(ctx context.Context, execution Execution, output json.RawMessage) (string, error) {
	return f(ctx, execution, output)
}

type profileStartArtifactResolverFunc func(context.Context, domain.InputManifest) ([]string, error)

func (f profileStartArtifactResolverFunc) ResolveStartArtifactKinds(ctx context.Context, manifest domain.InputManifest) ([]string, error) {
	return f(ctx, manifest)
}

func profileRuntimeExecution(profile domain.WorkflowExecutionProfileRef, node domain.NodeDefinition) Execution {
	runID := uuid.NewString()
	run := RunRecord{
		ID: runID, ProjectID: uuid.NewString(), ExecutionProfile: profile,
		Definition: domain.WorkflowDefinitionRef{ID: uuid.NewString(), Version: 1, Hash: strings.Repeat("a", 64), ExecutionProfile: profile},
		Scope:      json.RawMessage(`{}`), Context: NewRunContext(), Nodes: map[string]*NodeRecord{},
	}
	run.Context.Nodes[node.ID] = NodeMetadata{DefinitionNodeID: node.ID, TimeoutNanos: int64(time.Minute)}
	record := NodeRecord{ID: uuid.NewString(), RunID: runID, Key: node.ID, DefinitionNodeID: node.ID, Type: node.Type}
	run.Nodes[node.ID] = &record
	inputs, err := domain.NewNodeInputEnvelope(nil)
	if err != nil {
		panic(err)
	}
	return Execution{Run: run, Node: record, Definition: node, Workflow: domain.WorkflowDefinition{ExecutionProfile: profile}, Inputs: inputs}
}

func profileManifestHook(marker string) BuildManifestHook {
	return profileBuildManifestHookFunc(func(_ context.Context, execution Execution) (BuildManifest, error) {
		manifest := BuildManifest{
			SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID,
			ManifestGroupKey: uuid.NewString(), SliceIDs: []string{"slice"}, BundleIDs: []string{"bundle"},
			Sources: []domain.ArtifactRef{platformRef("profile-runtime-source")}, Constraints: json.RawMessage(`{"runtime":"` + marker + `"}`), CreatedAt: time.Now().UTC(),
		}
		return manifest, nil
	})
}

func installProfileDispatch(t *testing.T, engine *Engine, marker string) *MapRegistry {
	t.Helper()
	runners := NewMapRegistry()
	if err := runners.Register(domain.NodeTransform, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"runtime":"` + marker + `"}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	compilers := NewBuildManifestRegistry()
	if err := compilers.Register(ManifestCompilerCapability{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}, profileManifestHook(marker)); err != nil {
		t.Fatal(err)
	}
	engine.Runners = runners
	engine.ManifestCompilers = compilers
	engine.ConditionEvaluator = ConditionEvaluatorFunc(func(context.Context, Execution, []domain.ConditionBranch) (string, error) {
		return marker, nil
	})
	installCompleteTestExecutionProfileRuntime(t, engine, runners)
	return runners
}

func TestSealedProfilesIgnoreMutableRunnerCompilerAndConditionRegistries(t *testing.T) {
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	installProfileDispatch(t, engine, "old")
	if err := engine.SealExecutionProfiles(); err != nil {
		t.Fatal(err)
	}
	installProfileDispatch(t, engine, "replacement")

	transform := domain.NodeDefinition{ID: "transform", Name: "Transform", Type: domain.NodeTransform, Transform: &domain.TransformNodeConfig{Transform: "selection_passthrough"}}
	condition := domain.NodeDefinition{ID: "condition", Name: "Condition", Type: domain.NodeCondition, Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{{Name: "old", Expression: "true"}, {Name: "replacement", Default: true}}}}
	compiler := domain.NodeDefinition{ID: "compiler", Name: "Compiler", Type: domain.NodeManifestCompiler, ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}}

	for _, profile := range []domain.WorkflowExecutionProfileRef{CurrentWorkflowExecutionProfileRef(), WorkflowExecutionProfileV1Ref(), LegacyWorkflowExecutionProfileRef()} {
		bundle, err := engine.executionProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		result, err := bundle.executeNode(context.Background(), engine, profileRuntimeExecution(profile, transform))
		if err != nil || string(result.Output) != `{"runtime":"old"}` {
			t.Fatalf("profile %s runner drifted after registry replacement: output=%s err=%v", profile.Version, result.Output, err)
		}
		result, err = bundle.executeNode(context.Background(), engine, profileRuntimeExecution(profile, condition))
		if err != nil || result.Branch != "old" {
			t.Fatalf("profile %s condition dispatch drifted: branch=%q err=%v", profile.Version, result.Branch, err)
		}
		result, err = bundle.executeNode(context.Background(), engine, profileRuntimeExecution(profile, compiler))
		if err != nil || result.BuildManifest == nil || string(result.BuildManifest.Constraints) != `{"runtime":"old"}` {
			t.Fatalf("profile %s compiler dispatch drifted: manifest=%+v err=%v", profile.Version, result.BuildManifest, err)
		}
	}
}

func TestSealedConditionProfilesPreserveLegacyContextAndUseTypedV1Inputs(t *testing.T) {
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	// A mutable bootstrap evaluator must not replace either versioned condition
	// contract when profiles are sealed.
	engine.ConditionEvaluator = ConditionEvaluatorFunc(func(context.Context, Execution, []domain.ConditionBranch) (string, error) {
		return "bootstrap", nil
	})
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	if err := engine.SealExecutionProfiles(); err != nil {
		t.Fatal(err)
	}
	condition := domain.NodeDefinition{ID: "condition", Name: "Condition", Type: domain.NodeCondition, Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{
		{Name: "yes", Expression: `{"path":"/nodes/brief/ready","op":"eq","value":true}`},
		{Name: "no", Default: true},
	}}}
	for _, test := range []struct {
		profile domain.WorkflowExecutionProfileRef
		branch  string
		errText string
	}{
		{profile: LegacyWorkflowExecutionProfileRef(), branch: "yes"},
		{profile: WorkflowExecutionProfileV1Ref(), errText: "forbidden"},
		{profile: CurrentWorkflowExecutionProfileRef(), errText: "forbidden"},
	} {
		bundle, err := engine.executionProfile(test.profile)
		if err != nil {
			t.Fatal(err)
		}
		execution := profileRuntimeExecution(test.profile, condition)
		execution.Run.Context.Nodes["brief"] = NodeMetadata{Output: json.RawMessage(`{"ready":true}`)}
		result, runErr := bundle.executeNode(context.Background(), engine, execution)
		if test.errText != "" {
			if runErr == nil || !strings.Contains(runErr.Error(), test.errText) {
				t.Fatalf("profile %s accepted legacy global condition context: result=%+v err=%v", test.profile.Version, result, runErr)
			}
			continue
		}
		if runErr != nil || result.Branch != test.branch {
			t.Fatalf("profile %s did not preserve legacy condition context: branch=%q err=%v", test.profile.Version, result.Branch, runErr)
		}
	}
}

func TestFutureProfileCanSealDifferentDispatchWithoutChangingCurrentV2(t *testing.T) {
	futureNodeType := domain.WorkflowNodeType("future_external")
	registry := NewWorkflowExecutionProfileRegistry()
	current := currentExecutionProfileBundle()
	if err := registry.Register(current); err != nil {
		t.Fatal(err)
	}
	v3 := currentExecutionProfileBundle()
	v3.Descriptor.Version = "workflow-engine/v3-test"
	v3.Descriptor.Components.RunnerDispatchID = "builtin-runner-dispatch/v3-test"
	v3.componentIdentity = v3.Descriptor.Components
	v3.Descriptor.Capabilities.NodeTypes = append(v3.Descriptor.Capabilities.NodeTypes, futureNodeType)
	v3.nodeDispatch[futureNodeType] = workflowNodeDispatchRunner
	v3.runtimeFactory = func(bootstrap workflowExecutionRuntime, descriptor WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error) {
		runtime := cloneExecutionRuntime(bootstrap)
		runtime.runners[domain.NodeTransform] = RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
			return WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"runtime":"v3"}`)}, nil
		})
		return runtime, nil
	}
	if err := registry.Register(v3); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	engine.ExecutionProfiles = registry
	runners := installProfileDispatch(t, engine, "v2")
	if err := runners.Register(futureNodeType, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"runtime":"v3-future"}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := engine.SealExecutionProfiles(); err != nil {
		t.Fatal(err)
	}

	node := domain.NodeDefinition{ID: "transform", Name: "Transform", Type: domain.NodeTransform, Transform: &domain.TransformNodeConfig{Transform: "selection_passthrough"}}
	currentBundle, err := registry.Resolve(CurrentWorkflowExecutionProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	currentResult, err := currentBundle.executeNode(context.Background(), engine, profileRuntimeExecution(CurrentWorkflowExecutionProfileRef(), node))
	if err != nil || string(currentResult.Output) != `{"runtime":"v2"}` {
		t.Fatalf("current v2 dispatch changed when v3 was registered: output=%s err=%v", currentResult.Output, err)
	}
	v3Ref, err := v3.Descriptor.Ref()
	if err != nil {
		t.Fatal(err)
	}
	v3Bundle, err := registry.Resolve(v3Ref)
	if err != nil {
		t.Fatal(err)
	}
	v3Result, err := v3Bundle.executeNode(context.Background(), engine, profileRuntimeExecution(v3Ref, node))
	if err != nil || string(v3Result.Output) != `{"runtime":"v3"}` {
		t.Fatalf("v3 did not retain its independent dispatch: output=%s err=%v", v3Result.Output, err)
	}
	futureNode := domain.NodeDefinition{ID: "future", Name: "Future", Type: futureNodeType}
	futureResult, err := v3Bundle.executeNode(context.Background(), engine, profileRuntimeExecution(v3Ref, futureNode))
	if err != nil || string(futureResult.Output) != `{"runtime":"v3-future"}` {
		t.Fatalf("v3 did not capture its newly declared runner: output=%s err=%v", futureResult.Output, err)
	}
}

func TestFutureProfileMissingDeclaredRunnerFailsSealClosed(t *testing.T) {
	futureNodeType := domain.WorkflowNodeType("future_missing_runner")
	registry := NewWorkflowExecutionProfileRegistry()
	if err := registry.Register(currentExecutionProfileBundle()); err != nil {
		t.Fatal(err)
	}
	v3 := currentExecutionProfileBundle()
	v3.Descriptor.Version = "workflow-engine/v3-missing-runner-test"
	v3.Descriptor.Components.RunnerDispatchID = "builtin-runner-dispatch/v3-missing-runner-test"
	v3.componentIdentity = v3.Descriptor.Components
	v3.Descriptor.Capabilities.NodeTypes = append(v3.Descriptor.Capabilities.NodeTypes, futureNodeType)
	v3.nodeDispatch[futureNodeType] = workflowNodeDispatchRunner
	v3.runtimeFactory = func(bootstrap workflowExecutionRuntime, _ WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error) {
		return cloneExecutionRuntime(bootstrap), nil
	}
	if err := registry.Register(v3); err != nil {
		t.Fatal(err)
	}
	v3Ref, err := v3.Descriptor.Ref()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	engine.ExecutionProfiles = registry
	installProfileDispatch(t, engine, "v2")
	if err := engine.SealExecutionProfiles(); err == nil || !strings.Contains(err.Error(), string(futureNodeType)) {
		t.Fatalf("profile seal accepted a missing declared runner: %v", err)
	}
	if registry.IsSealed() {
		t.Fatal("failed profile seal published a partially bound registry")
	}
	if _, err := registry.Resolve(v3Ref); err == nil {
		t.Fatal("profile with a missing runner became resolvable")
	}
}

func TestProfileRegistrationRequiresExplicitDispatchOwnerForEveryNodeType(t *testing.T) {
	v3 := currentExecutionProfileBundle()
	v3.Descriptor.Version = "workflow-engine/v3-unowned-node-test"
	v3.Descriptor.Components.RunnerDispatchID = "builtin-runner-dispatch/v3-unowned-node-test"
	v3.componentIdentity = v3.Descriptor.Components
	v3.Descriptor.Capabilities.NodeTypes = append(
		v3.Descriptor.Capabilities.NodeTypes,
		domain.WorkflowNodeType("future_unowned"),
	)
	if err := NewWorkflowExecutionProfileRegistry().Register(v3); err == nil || !strings.Contains(err.Error(), "explicit dispatch owner") {
		t.Fatalf("profile registration accepted an unowned node capability: %v", err)
	}
}

func TestProfileSealRequiresDescriptorCompilerAndConditionBindings(t *testing.T) {
	t.Run("compiler", func(t *testing.T) {
		engine, err := NewEngine(NewMemoryStore(nil))
		if err != nil {
			t.Fatal(err)
		}
		fallback := map[domain.WorkflowNodeType]WorkerRunner{}
		for nodeType, owner := range frozenV0V1NodeDispatchOwnership(CurrentWorkflowExecutionProfileDescriptor()) {
			if owner == workflowNodeDispatchRunner {
				fallback[nodeType] = RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
					return WorkerResult{Disposition: ResultComplete}, nil
				})
			}
		}
		engine.Runners = testRunnerRegistryWithFallback{fallback: fallback}
		if err := engine.SealExecutionProfiles(); err == nil || !strings.Contains(err.Error(), "manifest compiler binding") {
			t.Fatalf("profile seal accepted a missing declared compiler: %v", err)
		}
	})

	t.Run("condition evaluator", func(t *testing.T) {
		registry := NewWorkflowExecutionProfileRegistry()
		v3 := currentExecutionProfileBundle()
		v3.Descriptor.Version = "workflow-engine/v3-condition-binding-test"
		v3.Descriptor.Components.ConditionAnalysisID = "condition-path-analysis/v3-test"
		v3.componentIdentity = v3.Descriptor.Components
		v3.Descriptor.Capabilities.NodeTypes = []domain.WorkflowNodeType{domain.NodeCondition}
		v3.Descriptor.Capabilities.ManifestCompilers = nil
		v3.nodeDispatch = map[domain.WorkflowNodeType]workflowNodeDispatchOwner{
			domain.NodeCondition: workflowNodeDispatchInterpreter,
		}
		v3.runtimeFactory = func(bootstrap workflowExecutionRuntime, _ WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error) {
			return cloneExecutionRuntime(bootstrap), nil
		}
		if err := registry.Register(v3); err != nil {
			t.Fatal(err)
		}
		engine, err := NewEngine(NewMemoryStore(nil))
		if err != nil {
			t.Fatal(err)
		}
		engine.ExecutionProfiles = registry
		if err := engine.SealExecutionProfiles(); err == nil || !strings.Contains(err.Error(), "condition evaluator binding") {
			t.Fatalf("profile seal accepted a missing declared condition evaluator: %v", err)
		}
	})
}

func TestSealedProfilesFreezeStartAndHumanControlPlaneValidators(t *testing.T) {
	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	install := func(marker string) {
		engine.StartArtifactKinds = profileStartArtifactResolverFunc(func(context.Context, domain.InputManifest) ([]string, error) { return []string{marker}, nil })
		engine.HumanEditOutput = HumanEditOutputValidatorFunc(func(context.Context, Execution, json.RawMessage, string) (HumanEditValidation, error) {
			return HumanEditValidation{ArtifactKind: marker}, nil
		})
		engine.WorkbenchCompletion = profileWorkbenchCompletionFunc(func(context.Context, Execution, json.RawMessage) (string, error) { return marker, nil })
		engine.ReviewGate = ReviewGateVerifierFunc(func(context.Context, string, []domain.ArtifactRef, domain.ReviewGateNodeConfig) error {
			if marker != "old" {
				t.Fatalf("replacement review validator was called")
			}
			return nil
		})
		engine.HumanWorkflowContextKeys = map[string]struct{}{marker: {}}
	}
	install("old")
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	if err := engine.SealExecutionProfiles(); err != nil {
		t.Fatal(err)
	}
	install("replacement")
	bundle, err := engine.executionProfile(CurrentWorkflowExecutionProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	runtime := bundle.executionRuntime(engine)
	kinds, err := runtime.startArtifactKinds.ResolveStartArtifactKinds(context.Background(), domain.InputManifest{})
	if err != nil || len(kinds) != 1 || kinds[0] != "old" {
		t.Fatalf("start resolver drifted after seal: kinds=%v err=%v", kinds, err)
	}
	edit, err := runtime.humanEditOutput.ValidateHumanEdit(context.Background(), Execution{}, json.RawMessage(`{}`), uuid.NewString())
	if err != nil || edit.ArtifactKind != "old" {
		t.Fatalf("HumanEdit validator drifted after seal: edit=%+v err=%v", edit, err)
	}
	revision, err := runtime.workbenchCompletion.ValidateCompletion(context.Background(), Execution{}, json.RawMessage(`{}`))
	if err != nil || revision != "old" {
		t.Fatalf("Workbench completion validator drifted after seal: revision=%q err=%v", revision, err)
	}
	if err := runtime.reviewGate.VerifyApproval(context.Background(), uuid.NewString(), nil, domain.ReviewGateNodeConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, exists := runtime.humanContextKeys["old"]; !exists {
		t.Fatal("sealed human workflow context allowlist lost its original key")
	}
	if _, exists := runtime.humanContextKeys["replacement"]; exists {
		t.Fatal("replacement human workflow context allowlist leaked into sealed runtime")
	}
}

func TestUnsealedOrMissingProfileRuntimeFailsClosed(t *testing.T) {
	registry, err := NewBuiltinWorkflowExecutionProfileRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(CurrentWorkflowExecutionProfileRef()); err == nil {
		t.Fatal("unsealed profile resolved without an executable bundle")
	}
	partial := NewWorkflowExecutionProfileRegistry()
	if err := partial.Register(currentExecutionProfileBundle()); err != nil {
		t.Fatal(err)
	}
	if err := partial.FinishSeal(); err == nil {
		t.Fatal("registry sealed while a profile runtime bundle was missing")
	}
}
