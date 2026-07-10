package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/generation"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

func platformHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
func platformRef(name string) domain.ArtifactRef {
	return domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: platformHash(name)}
}

type fakeCoreProposals struct {
	manifest domain.InputManifest
	created  core.CreateManifestInput
}

func (f *fakeCoreProposals) GetManifest(context.Context, string, string) (domain.InputManifest, error) {
	return f.manifest, nil
}
func (f *fakeCoreProposals) CreateManifest(_ context.Context, projectID, actorID string, input core.CreateManifestInput) (domain.InputManifest, error) {
	f.created = input
	sources := make([]domain.ManifestSource, len(input.Sources))
	for index, source := range input.Sources {
		sources[index] = domain.ManifestSource{Ref: fromCoreVersionRef(source.Ref), Purpose: source.Purpose}
	}
	var base *domain.ArtifactRef
	if input.BaseRevision != nil {
		value := fromCoreVersionRef(*input.BaseRevision)
		base = &value
	}
	return domain.NewInputManifest(uuid.NewString(), projectID, input.JobType, input.DeliverySliceID, base, sources, input.Constraints, input.OutputSchemaVersion, actorID, time.Now())
}

func adapterExecution(t *testing.T) (Execution, domain.InputManifest) {
	t.Helper()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	base := platformRef("base")
	source := platformRef("source")
	manifest, err := domain.NewInputManifest(uuid.NewString(), projectID, "initial", "", &base, []domain.ManifestSource{{Ref: source, Purpose: "approved requirements"}}, json.RawMessage(`{"strict":true}`), "input/v1", actorID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	definition := domain.NodeDefinition{ID: "ai", Name: "AI", Type: domain.NodeAITransform, InputSchema: engineSchema(), OutputSchema: engineSchema(), AITransform: &domain.AITransformNodeConfig{JobType: "decompose_pages", ModelPolicy: "default", OutputSchemaVersion: "blueprint/v1", MaxAttempts: 2, Timeout: time.Minute}}
	contextState := NewRunContext()
	contextState.Nodes["ai"] = NodeMetadata{DefinitionNodeID: "ai", MaxAttempts: 2, TimeoutNanos: int64(time.Minute)}
	execution := Execution{Run: RunRecord{ID: uuid.NewString(), ProjectID: projectID, Definition: domain.WorkflowDefinitionRef{ID: uuid.NewString(), Version: 1, Hash: platformHash("definition")}, InputManifest: ptrManifest(manifest.Ref()), Status: RunRunning, Scope: json.RawMessage(`{"feature":"orders"}`), Context: contextState, StartedBy: actorID, Nodes: map[string]*NodeRecord{}}, Node: NodeRecord{ID: uuid.NewString(), Key: "ai", DefinitionNodeID: "ai", Type: domain.NodeAITransform, Status: NodeRunning}, Definition: definition}
	return execution, manifest
}
func ptrManifest(value domain.ManifestRef) *domain.ManifestRef { return &value }

type fakeRequirementBaselineCompiler struct {
	sources []core.VersionRef
	result  core.ArtifactRevision
}

type fakeTargetInitializer struct {
	sources []core.ManifestSourceInput
	result  *core.VersionRef
}

func (f *fakeTargetInitializer) EnsureTarget(
	_ context.Context,
	_ Execution,
	_ string,
	sources []core.ManifestSourceInput,
) (*core.VersionRef, error) {
	f.sources = append([]core.ManifestSourceInput(nil), sources...)
	return f.result, nil
}

func (f *fakeRequirementBaselineCompiler) Compile(_ context.Context, _, _ string, sources []core.VersionRef) (core.ArtifactRevision, error) {
	f.sources = append([]core.VersionRef(nil), sources...)
	return f.result, nil
}

func adapterInputs(t *testing.T, execution Execution, output json.RawMessage, slices ...SliceContext) domain.NodeInputEnvelope {
	t.Helper()
	refs := make([]domain.WorkflowSliceRef, 0, len(slices))
	for _, slice := range slices {
		refs = append(refs, workflowSliceRef(slice))
	}
	envelope, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "incoming", FromPort: "default", ToPort: "default", Output: output, Value: output,
		Source: domain.NodeOutputReference{RunID: execution.Run.ID, NodeKey: "upstream", DefinitionNodeID: "upstream", DeliverySliceRefs: refs},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestCoreManifestFreezerCompilesExactRequirementsIntoBaseline(t *testing.T) {
	execution, upstream := adapterExecution(t)
	proposals := &fakeCoreProposals{manifest: upstream}
	baselineRef := platformRef("baseline")
	baseline := &fakeRequirementBaselineCompiler{result: core.ArtifactRevision{
		ID: baselineRef.RevisionID, ArtifactID: baselineRef.ArtifactID, ContentHash: baselineRef.ContentHash,
	}}
	target := &fakeTargetInitializer{}
	frozen, err := (CoreManifestFreezer{
		Proposals: proposals, RequirementBaseline: baseline, Targets: target,
	}).Freeze(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.sources) != 1 || fromCoreVersionRef(baseline.sources[0]) != upstream.Sources[0].Ref {
		t.Fatalf("baseline did not receive the exact approved requirement source: %+v", baseline.sources)
	}
	if len(frozen.Sources) != 1 || frozen.Sources[0].Ref != baselineRef || frozen.Sources[0].Purpose != "requirement_baseline" {
		t.Fatalf("blueprint manifest did not consume only the compiled Requirement Baseline: %+v", frozen.Sources)
	}
	if len(target.sources) != 1 || target.sources[0].Ref.ArtifactID != baselineRef.ArtifactID ||
		target.sources[0].Ref.RevisionID != baselineRef.RevisionID || target.sources[0].Ref.ContentHash != baselineRef.ContentHash {
		t.Fatalf("Blueprint target was initialized before the exact Requirement Baseline was frozen: %+v", target.sources)
	}
	if frozen.BaseRevision == nil || !frozen.BaseRevision.Equal(*upstream.BaseRevision) {
		t.Fatalf("proposal base revision changed while compiling the baseline: %+v", frozen.BaseRevision)
	}
	if proposals.created.JobType != "decompose_pages" || proposals.created.OutputSchemaVersion != "blueprint/v1" {
		t.Fatalf("typed AI config was not compiled: %+v", proposals.created)
	}
	if frozen.Ref() == upstream.Ref() {
		t.Fatal("freezer must create a new immutable manifest")
	}
}

type fakeArtifactGenerator struct {
	manifest domain.InputManifest
	called   bool
}

func (f *fakeArtifactGenerator) GenerateArtifactProposal(_ context.Context, manifestID, actorID, model string) (generation.ArtifactGenerationResult, error) {
	f.called = true
	proposal, err := domain.NewOutputProposal(uuid.NewString(), f.manifest.ProjectID, f.manifest.BaseRevision.ArtifactID, f.manifest.Ref(), *f.manifest.BaseRevision, []domain.ProposalOperation{{ID: "op", Kind: domain.OperationReplace, Path: "/title", Value: json.RawMessage(`"new"`)}}, nil, nil, actorID, time.Now())
	if err != nil {
		return generation.ArtifactGenerationResult{}, err
	}
	return generation.ArtifactGenerationResult{Proposal: *proposal, Provider: "fake", Model: model, Usage: &ai.Usage{}}, nil
}
func TestGenerationDispatcherRecordsProposalWithoutApplyingArtifact(t *testing.T) {
	execution, manifest := adapterExecution(t)
	generator := &fakeArtifactGenerator{manifest: manifest}
	ref, err := (GenerationProposalDispatcher{Generation: generator, DefaultModel: "test-model"}).Dispatch(context.Background(), execution, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !generator.called || ref == nil || !domain.IsCanonicalHash(ref.PayloadHash) {
		t.Fatalf("proposal was not recorded: %+v", ref)
	}
}

func TestTargetArtifactTemplatesAreStableAndSliceScoped(t *testing.T) {
	t.Parallel()
	execution, _ := adapterExecution(t)
	execution.Node.Key = "prototype-ai:slice-1"
	execution.Run.Context.Nodes[execution.Node.Key] = NodeMetadata{DefinitionNodeID: "prototype-ai", SliceID: "slice-1"}
	execution.Run.Context.Slices["slice-1"] = SliceContext{ID: "slice-1", Key: "page.home", Title: "Home"}
	kind, key, title, content, ok := targetArtifactTemplate(execution, "generate_prototype")
	if !ok || kind != "prototype" || key != "PROTOTYPE-PAGE-HOME" || title != "Prototype · Home" || len(content) == 0 {
		t.Fatalf("prototype target = %q %q %q %s %v", kind, key, title, content, ok)
	}
}

type fakeCoreTargetArtifacts struct {
	projectID     string
	artifacts     map[string]core.VersionedArtifact
	byKind        map[string][]core.Artifact
	createdInputs []core.CreateArtifactInput
}

func newFakeCoreTargetArtifacts(projectID string) *fakeCoreTargetArtifacts {
	return &fakeCoreTargetArtifacts{
		projectID: projectID, artifacts: map[string]core.VersionedArtifact{}, byKind: map[string][]core.Artifact{},
	}
}

func (f *fakeCoreTargetArtifacts) addSource(reference core.VersionRef, kind string) {
	f.artifacts[reference.ArtifactID] = core.VersionedArtifact{Artifact: core.Artifact{
		ID: reference.ArtifactID, ProjectID: f.projectID, Kind: kind,
	}}
}

func (f *fakeCoreTargetArtifacts) addExistingTarget(value core.VersionedArtifact) {
	f.artifacts[value.Artifact.ID] = value
	f.byKind[value.Artifact.Kind] = append(f.byKind[value.Artifact.Kind], value.Artifact)
}

func (f *fakeCoreTargetArtifacts) Create(
	_ context.Context,
	projectID string,
	actorID string,
	input core.CreateArtifactInput,
) (core.VersionedArtifact, error) {
	f.createdInputs = append(f.createdInputs, input)
	artifactID, draftID := uuid.NewString(), uuid.NewString()
	sources := make([]core.ArtifactSource, len(input.SourceVersions))
	for index, source := range input.SourceVersions {
		sources[index] = core.ArtifactSource{VersionRef: source.Ref, Purpose: source.Purpose, Required: source.Required}
	}
	value := core.VersionedArtifact{
		Artifact: core.Artifact{ID: artifactID, ProjectID: projectID, Kind: input.Kind, ArtifactKey: input.ArtifactKey, Title: input.Title},
		Draft: &core.ArtifactDraft{
			ID: draftID, ArtifactID: artifactID, Content: append(json.RawMessage(nil), input.Content...),
			ContentHash: platformHash(string(input.Content)), SourceVersions: sources, ETag: `"draft:target:1"`,
			CreatedBy: actorID, UpdatedBy: actorID,
		},
	}
	f.addExistingTarget(value)
	return value, nil
}

func (f *fakeCoreTargetArtifacts) List(_ context.Context, _, _ string, kind, _ string) ([]core.Artifact, error) {
	return append([]core.Artifact(nil), f.byKind[kind]...), nil
}

func (f *fakeCoreTargetArtifacts) Get(_ context.Context, artifactID, _ string, _ bool) (core.VersionedArtifact, error) {
	value, ok := f.artifacts[artifactID]
	if !ok {
		return core.VersionedArtifact{}, core.ErrNotFound
	}
	return value, nil
}

func (f *fakeCoreTargetArtifacts) CreateRevision(
	_ context.Context,
	artifactID string,
	_ string,
	_ string,
	_ core.CreateRevisionInput,
) (core.ArtifactRevision, error) {
	value := f.artifacts[artifactID]
	if value.Draft == nil {
		return core.ArtifactRevision{}, core.ErrNotFound
	}
	return core.ArtifactRevision{
		ID: uuid.NewString(), ArtifactID: artifactID, ContentHash: value.Draft.ContentHash,
		Content: value.Draft.Content, SourceVersions: value.Draft.SourceVersions,
	}, nil
}

func TestCoreTargetArtifactInitializerPinsGovernedLineage(t *testing.T) {
	t.Parallel()

	execution, _ := adapterExecution(t)
	baseline := toCoreVersionRef(platformRef("current-approved-baseline"))
	blueprint := toCoreVersionRef(platformRef("approved-blueprint"))
	pageAnchor := "page-orders"
	blueprint.AnchorID = &pageAnchor
	pageSpec := toCoreVersionRef(platformRef("current-approved-page-spec"))
	unrelated := toCoreVersionRef(platformRef("unrelated-requirements"))

	t.Run("blueprint", func(t *testing.T) {
		artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
		artifacts.addSource(baseline, "requirement_baseline")
		initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}
		if _, err := initializer.EnsureTarget(context.Background(), execution, "decompose_pages", []core.ManifestSourceInput{
			{Ref: baseline, Purpose: "requirement_baseline"},
		}); err != nil {
			t.Fatal(err)
		}
		input := artifacts.createdInputs[0]
		if input.Kind != "blueprint" || len(input.SourceVersions) != 1 ||
			!sameCoreVersionRef(input.SourceVersions[0].Ref, baseline) || !input.SourceVersions[0].Required {
			t.Fatalf("Blueprint target lost exact baseline lineage: %+v", input)
		}
	})

	t.Run("page_spec", func(t *testing.T) {
		pageExecution := execution
		pageExecution.Node.Key = "page-spec-ai:slice-orders"
		pageExecution.Node.SliceID = "slice-orders"
		pageExecution.Run.Context.Nodes[pageExecution.Node.Key] = NodeMetadata{DefinitionNodeID: "page-spec-ai", SliceID: "slice-orders"}
		pageExecution.Run.Context.Slices["slice-orders"] = SliceContext{
			ID: "slice-orders", Key: "page.orders", Title: "Orders",
			Payload: json.RawMessage(`{"route":"/orders","userGoal":"Manage orders"}`),
		}
		artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
		artifacts.addSource(blueprint, "blueprint")
		artifacts.addSource(unrelated, "product_requirements")
		initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}
		if _, err := initializer.EnsureTarget(context.Background(), pageExecution, "generate_page_spec", []core.ManifestSourceInput{
			{Ref: unrelated, Purpose: "workflow_input"}, {Ref: blueprint, Purpose: "delivery_slice_blueprint"},
		}); err != nil {
			t.Fatal(err)
		}
		input := artifacts.createdInputs[0]
		var content struct {
			BlueprintPageNodeID string `json:"blueprintPageNodeId"`
			Route               string `json:"route"`
		}
		if json.Unmarshal(input.Content, &content) != nil || content.BlueprintPageNodeID != pageAnchor || content.Route != "/orders" ||
			len(input.SourceVersions) != 1 || !sameCoreVersionRef(input.SourceVersions[0].Ref, blueprint) {
			t.Fatalf("PageSpec target lost exact anchored Blueprint lineage: input=%+v content=%s", input, input.Content)
		}
	})

	t.Run("prototype", func(t *testing.T) {
		prototypeExecution := execution
		prototypeExecution.Node.Key = "prototype-ai:slice-orders"
		prototypeExecution.Node.SliceID = "slice-orders"
		prototypeExecution.Run.Context.Nodes[prototypeExecution.Node.Key] = NodeMetadata{DefinitionNodeID: "prototype-ai", SliceID: "slice-orders"}
		prototypeExecution.Run.Context.Slices["slice-orders"] = SliceContext{
			ID: "slice-orders", Key: "page.orders", Title: "Orders", Payload: json.RawMessage(`{"exploratory":true}`),
		}
		artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
		artifacts.addSource(pageSpec, "page_spec")
		artifacts.addSource(blueprint, "blueprint")
		initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}
		if _, err := initializer.EnsureTarget(context.Background(), prototypeExecution, "generate_prototype", []core.ManifestSourceInput{
			{Ref: blueprint, Purpose: "delivery_slice_blueprint"}, {Ref: pageSpec, Purpose: "delivery_slice_page_spec"},
		}); err != nil {
			t.Fatal(err)
		}
		input := artifacts.createdInputs[0]
		var content struct {
			PageSpecRevision core.VersionRef `json:"pageSpecRevision"`
			Exploratory      bool            `json:"exploratory"`
		}
		if json.Unmarshal(input.Content, &content) != nil || !sameCoreVersionRef(content.PageSpecRevision, pageSpec) || !content.Exploratory ||
			len(input.SourceVersions) != 1 || !sameCoreVersionRef(input.SourceVersions[0].Ref, pageSpec) {
			t.Fatalf("Prototype target lost exact PageSpec lineage or exploratory policy: input=%+v content=%s", input, input.Content)
		}
	})
}

func TestCoreTargetArtifactInitializerFailsClosedOnMissingOrWrongLineage(t *testing.T) {
	t.Parallel()

	execution, _ := adapterExecution(t)
	requirements := toCoreVersionRef(platformRef("ordinary-requirements"))
	pageSpecA := toCoreVersionRef(platformRef("page-a"))
	pageSpecB := toCoreVersionRef(platformRef("page-b"))
	wholeBlueprint := toCoreVersionRef(platformRef("whole-blueprint"))

	for _, testCase := range []struct {
		name      string
		jobType   string
		sources   []core.ManifestSourceInput
		configure func(*fakeCoreTargetArtifacts)
	}{
		{
			name: "blueprint_wrong_kind", jobType: "decompose_pages",
			sources:   []core.ManifestSourceInput{{Ref: requirements}},
			configure: func(artifacts *fakeCoreTargetArtifacts) { artifacts.addSource(requirements, "product_requirements") },
		},
		{
			name: "page_spec_unanchored_blueprint", jobType: "generate_page_spec",
			sources:   []core.ManifestSourceInput{{Ref: wholeBlueprint}},
			configure: func(artifacts *fakeCoreTargetArtifacts) { artifacts.addSource(wholeBlueprint, "blueprint") },
		},
		{
			name: "prototype_multiple_page_specs", jobType: "generate_prototype",
			sources: []core.ManifestSourceInput{{Ref: pageSpecA}, {Ref: pageSpecB}},
			configure: func(artifacts *fakeCoreTargetArtifacts) {
				artifacts.addSource(pageSpecA, "page_spec")
				artifacts.addSource(pageSpecB, "page_spec")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
			testCase.configure(artifacts)
			initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}
			if _, err := initializer.EnsureTarget(context.Background(), execution, testCase.jobType, testCase.sources); err == nil {
				t.Fatal("invalid immutable target lineage was accepted")
			}
			if len(artifacts.createdInputs) != 0 {
				t.Fatalf("invalid lineage created a target: %+v", artifacts.createdInputs)
			}
		})
	}
}

type fakeWorkbench struct {
	calls      int
	prototypes []core.VersionRef
}

func (f *fakeWorkbench) CreateBundle(_ context.Context, projectID, actorID string, input core.CreateWorkbenchBundleInput) (core.WorkbenchBundle, error) {
	f.calls++
	prototype := input.PrototypeRevision
	f.prototypes = append(f.prototypes, prototype)
	return core.WorkbenchBundle{ID: uuid.NewString(), ProjectID: projectID, PrototypeRevision: prototype, PageSpecRevision: toCoreVersionRef(platformRef("page")), BlueprintRevision: toCoreVersionRef(platformRef("blueprint")), RequirementRevisions: []core.VersionRef{toCoreVersionRef(platformRef("requirements"))}, CreatedBy: actorID, CreatedAt: time.Now()}, nil
}

func TestWorkbenchHookCreatesPerSliceBundlesAndFrozenBuildManifest(t *testing.T) {
	execution, _ := adapterExecution(t)
	execution.Definition = domain.NodeDefinition{ID: "compile", Name: "Compile", Type: domain.NodeManifestCompiler, InputSchema: engineSchema(), OutputSchema: engineSchema(), ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "v1"}}
	prototypeA, prototypeB := platformRef("prototype-a"), platformRef("prototype-b")
	sliceA := SliceContext{ID: uuid.NewString(), Key: "a", Title: "A", FanOutNodeID: "pages", Blueprint: platformRef("bp-a"), Prototype: &prototypeA}
	sliceB := SliceContext{ID: uuid.NewString(), Key: "b", Title: "B", FanOutNodeID: "pages", Blueprint: platformRef("bp-b"), Prototype: &prototypeB}
	execution.Run.Context.Slices = map[string]SliceContext{sliceA.ID: sliceA, sliceB.ID: sliceB}
	execution.Inputs = adapterInputs(t, execution, json.RawMessage(`{}`), sliceA, sliceB)
	service := &fakeWorkbench{}
	manifest, err := (CoreWorkbenchManifestHook{Workbench: service}).Compile(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if service.calls != 2 || len(manifest.BundleIDs) != 2 || len(manifest.Sources) < 6 {
		t.Fatalf("unexpected build manifest: %+v", manifest)
	}
}

type fakeImplementationGenerator struct {
	calls        int
	instructions []string
}

func (f *fakeImplementationGenerator) GenerateImplementation(_ context.Context, bundleID, actorID, model, instruction string) (generation.ImplementationGenerationResult, error) {
	f.calls++
	f.instructions = append(f.instructions, instruction)
	return generation.ImplementationGenerationResult{Proposal: core.ImplementationProposal{ID: uuid.NewString(), BuildManifestID: bundleID, Status: "open", PayloadHash: platformHash(bundleID), CreatedBy: actorID}, Model: model}, nil
}
func TestWorkbenchRunnerOnlyReturnsImplementationProposalRefs(t *testing.T) {
	execution, _ := adapterExecution(t)
	manifest := BuildManifest{SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID, SliceIDs: []string{uuid.NewString(), uuid.NewString()}, BundleIDs: []string{uuid.NewString(), uuid.NewString()}, Sources: []domain.ArtifactRef{platformRef("source")}, CreatedAt: time.Now()}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	execution.Inputs = adapterInputs(t, execution, mustJSON(manifest))
	generator := &fakeImplementationGenerator{}
	result, err := (GenerationWorkbenchRunner{Generation: generator, DefaultModel: "model"}).Run(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 2 || result.Disposition != ResultWaitInput || len(result.Output) == 0 {
		t.Fatalf("unexpected workbench result: %+v", result)
	}
}

func TestWorkbenchRunnerConsumesReviewedConversationInstructionFromScope(t *testing.T) {
	execution, _ := adapterExecution(t)
	manifest := BuildManifest{SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID, SliceIDs: []string{uuid.NewString()}, BundleIDs: []string{uuid.NewString()}, Sources: []domain.ArtifactRef{platformRef("source")}, CreatedAt: time.Now()}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	execution.Inputs = adapterInputs(t, execution, mustJSON(manifest))
	execution.Run.Scope = json.RawMessage(`{"conversationIntent":{"workbenchInstruction":{"objective":"Build the approved dashboard","constraints":["Use exact API contracts"]}}}`)
	generator := &fakeImplementationGenerator{}
	if _, err := (GenerationWorkbenchRunner{Generation: generator, DefaultModel: "model", Instruction: "fallback"}).Run(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	if len(generator.instructions) != 1 || !strings.Contains(generator.instructions[0], "Build the approved dashboard") || strings.Contains(generator.instructions[0], "fallback") {
		t.Fatalf("reviewed conversation instruction was not used: %#v", generator.instructions)
	}
}

func TestPublishRunnerUsesTypedQualityBranchNotGlobalManifest(t *testing.T) {
	execution, _ := adapterExecution(t)
	actorID := uuid.NewString()
	execution.Node = NodeRecord{ID: uuid.NewString(), Key: "publish", DefinitionNodeID: "publish", Type: domain.NodePublish, Status: NodeRunning}
	execution.Definition = domain.NodeDefinition{
		ID: "publish", Name: "Publish", Type: domain.NodePublish,
		InputSchema: engineSchema(), OutputSchema: engineSchema(),
		Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "admin"},
	}
	execution.Run.Nodes["publish"] = &execution.Node
	execution.Run.Context.Nodes["publish"] = NodeMetadata{DefinitionNodeID: "publish", ExecutionActor: &ActorProvenance{
		ActorID: actorID, Role: core.RoleAdmin, Action: core.ActionPublish,
		Source: ActorSourceAuthenticatedCommand, AuthorizedAt: time.Now().UTC(),
	}}
	selected := BuildManifest{
		SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID,
		SliceIDs: []string{"selected-slice"}, BundleIDs: []string{"selected-bundle"},
		Sources: []domain.ArtifactRef{platformRef("selected")}, CreatedAt: time.Now().UTC(),
	}
	if err := selected.Freeze(); err != nil {
		t.Fatal(err)
	}
	unrelated := selected
	unrelated.SliceIDs = []string{"unrelated-slice"}
	unrelated.BundleIDs = []string{"unrelated-bundle"}
	unrelated.Sources = []domain.ArtifactRef{platformRef("unrelated")}
	unrelated.Hash = ""
	if err := unrelated.Freeze(); err != nil {
		t.Fatal(err)
	}
	execution.Run.Context.Values["buildManifest"] = mustJSON(unrelated)
	workspace := platformRef("workspace")
	quality := QualityResult{
		Passed: true, QualityRunID: uuid.NewString(), WorkspaceRevision: &workspace,
		BuildManifest: &selected,
	}
	execution.Inputs = adapterInputs(t, execution, mustJSON(quality))
	var received WorkflowPublishInput
	runner := PublishRunner{
		Access: actorTestAccess{roles: map[string]core.Role{actorID: core.RoleAdmin}},
		Publisher: PublisherFunc(func(_ context.Context, _, _, _ string, _ string, input WorkflowPublishInput) (PublishResult, error) {
			received = input
			return PublishResult{URL: "/published/selected", DeploymentID: uuid.NewString()}, nil
		}),
	}
	if _, err := runner.Run(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	if received.BuildManifest.Hash != selected.Hash || received.BuildManifest.Hash == unrelated.Hash || received.WorkspaceRevision != workspace {
		t.Fatalf("publish crossed workflow branches: received=%+v selected=%s unrelated=%s", received, selected.Hash, unrelated.Hash)
	}
}

type fakeContentStore struct {
	items map[string]content.StoredContent
}

func (f *fakeContentStore) PutPending(_ context.Context, projectID, aggregateType, aggregateID string, schemaVersion int, payload json.RawMessage) (content.Reference, error) {
	if f.items == nil {
		f.items = map[string]content.StoredContent{}
	}
	id := uuid.NewString()
	hash := platformHash(string(payload))
	reference := content.Reference{ID: id, ContentHash: hash, SchemaVersion: schemaVersion}
	f.items[id] = content.StoredContent{Reference: reference, ProjectID: projectID, AggregateType: aggregateType, AggregateID: aggregateID, State: content.StatePending, Payload: append(json.RawMessage(nil), payload...)}
	return reference, nil
}
func (f *fakeContentStore) Finalize(_ context.Context, id string) error {
	item := f.items[id]
	item.State = content.StateFinalized
	f.items[id] = item
	return nil
}
func (f *fakeContentStore) Abort(_ context.Context, id string) error { delete(f.items, id); return nil }
func (f *fakeContentStore) Get(_ context.Context, id, hash string) (content.StoredContent, error) {
	item, ok := f.items[id]
	if !ok {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if hash != "" && item.ContentHash != hash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	return item, nil
}
func TestCoreContentAdapterFinalizesAndVerifiesSharedPayload(t *testing.T) {
	store := &fakeContentStore{}
	adapter := CoreContentStoreAdapter{Store: store}
	payload := []byte(`{"projectId":"` + uuid.NewString() + `","value":1}`)
	kind, ref, hash, err := adapter.Put(context.Background(), "manifest", uuid.NewString(), payload)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := adapter.Get(context.Background(), kind, ref, hash)
	if err != nil || string(loaded) != string(payload) {
		t.Fatalf("content roundtrip failed: %v", err)
	}
	if _, err := adapter.Get(context.Background(), kind, ref, platformHash("other")); !errors.Is(err, content.ErrHashMismatch) {
		t.Fatalf("expected hash guard, got %v", err)
	}
}
