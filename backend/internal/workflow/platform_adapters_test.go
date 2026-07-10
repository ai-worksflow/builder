package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/generation"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
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

func TestCoreManifestFreezerAllowsBaseOnlyProjectBriefRefinement(t *testing.T) {
	t.Parallel()
	execution, _ := adapterExecution(t)
	brief := platformRef("entry-project-brief")
	upstream, err := domain.NewInputManifest(
		uuid.NewString(), execution.Run.ProjectID, "workflow_start", "", &brief,
		[]domain.ManifestSource{{Ref: brief, Purpose: "project_brief"}}, json.RawMessage(`{}`),
		"workflow-input/v1", execution.Run.StartedBy, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	execution.Run.InputManifest = ptrManifest(upstream.Ref())
	execution.Run.Scope = json.RawMessage(`{"conversationIntent":{"conversationId":"conversation","proposalId":"intent-proposal","workbenchInstruction":{"objective":"Refine the reviewed brief"}}}`)
	execution.Definition.AITransform.JobType = "refine_project_brief"
	execution.Definition.AITransform.OutputSchemaVersion = "project-brief-proposal/v1"
	proposals := &fakeCoreProposals{manifest: upstream}
	frozen, err := (CoreManifestFreezer{Proposals: proposals}).Freeze(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.BaseRevision == nil || !frozen.BaseRevision.Equal(brief) || len(frozen.Sources) != 0 {
		t.Fatalf("base-only Project Brief manifest was not frozen exactly: %+v", frozen)
	}
	if proposals.created.BaseRevision == nil || !sameCoreVersionRef(*proposals.created.BaseRevision, toCoreVersionRef(brief)) || len(proposals.created.Sources) != 0 || !strings.Contains(string(proposals.created.Constraints), "intent-proposal") {
		t.Fatalf("reviewed conversation intent/base did not reach the immutable refine manifest: %+v", proposals.created)
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
	revisionCalls int
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
			Status: "draft", CreatedBy: actorID, UpdatedBy: actorID,
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
	f.revisionCalls++
	revisionID := uuid.NewString()
	var parent *string
	if value.LatestRevision != nil {
		id := value.LatestRevision.ID
		parent = &id
	}
	revision := core.ArtifactRevision{
		ID: revisionID, ArtifactID: artifactID, ParentRevisionID: parent,
		RevisionNumber: uint64(f.revisionCalls), ContentHash: value.Draft.ContentHash,
		Content:        append(json.RawMessage(nil), value.Draft.Content...),
		SourceVersions: append([]core.ArtifactSource(nil), value.Draft.SourceVersions...),
	}
	value.LatestRevision = &revision
	value.Artifact.LatestRevisionID = &revisionID
	value.Draft.BaseRevisionID = &revisionID
	f.artifacts[artifactID] = value
	for index := range f.byKind[value.Artifact.Kind] {
		if f.byKind[value.Artifact.Kind][index].ID == artifactID {
			f.byKind[value.Artifact.Kind][index] = value.Artifact
		}
	}
	return revision, nil
}

func (f *fakeCoreTargetArtifacts) applyAndCheckpoint(
	t *testing.T,
	artifactID string,
	input core.CreateArtifactInput,
) core.ArtifactRevision {
	t.Helper()
	value, exists := f.artifacts[artifactID]
	if !exists || value.Draft == nil {
		t.Fatalf("target %s has no draft to apply", artifactID)
	}
	sources := make([]core.ArtifactSource, len(input.SourceVersions))
	for index, source := range input.SourceVersions {
		sources[index] = core.ArtifactSource{VersionRef: source.Ref, Purpose: source.Purpose, Required: source.Required}
	}
	value.Draft.Content = append(json.RawMessage(nil), input.Content...)
	value.Draft.ContentHash = platformHash(string(input.Content))
	value.Draft.SourceVersions = sources
	value.Draft.ETag = fmt.Sprintf(`"draft:target:%d"`, f.revisionCalls+1)
	f.artifacts[artifactID] = value
	revision, err := f.CreateRevision(context.Background(), artifactID, "", value.Draft.ETag, core.CreateRevisionInput{})
	if err != nil {
		t.Fatal(err)
	}
	return revision
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

func TestPrototypeTargetKeysDoNotCollideAfterSliceKeyNormalization(t *testing.T) {
	t.Parallel()
	execution, _ := adapterExecution(t)
	artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
	pageSpecA := toCoreVersionRef(platformRef("page-spec-a"))
	pageSpecB := toCoreVersionRef(platformRef("page-spec-b"))
	artifacts.addSource(pageSpecA, "page_spec")
	artifacts.addSource(pageSpecB, "page_spec")
	initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}

	for index, item := range []struct {
		sliceID string
		key     string
		page    core.VersionRef
	}{
		{sliceID: "slice-a", key: "page.orders", page: pageSpecA},
		{sliceID: "slice-b", key: "page-orders", page: pageSpecB},
	} {
		current := execution
		current.Node.Key = "prototype-ai:" + item.sliceID
		current.Node.SliceID = item.sliceID
		current.Run.Context.Nodes[current.Node.Key] = NodeMetadata{DefinitionNodeID: "prototype-ai", SliceID: item.sliceID}
		current.Run.Context.Slices[item.sliceID] = SliceContext{ID: item.sliceID, Key: item.key, Title: item.key}
		if _, err := initializer.EnsureTarget(context.Background(), current, "generate_prototype", []core.ManifestSourceInput{{Ref: item.page, Purpose: "delivery_slice_page_spec"}}); err != nil {
			t.Fatalf("initialize colliding prototype %d: %v", index, err)
		}
	}
	if len(artifacts.createdInputs) != 2 {
		t.Fatalf("normalized slice-key collision reused a prototype target: %+v", artifacts.createdInputs)
	}
	first, second := artifacts.createdInputs[0].ArtifactKey, artifacts.createdInputs[1].ArtifactKey
	if first == second || !strings.HasPrefix(first, "PROTOTYPE-PAGE-ORDERS-") || !strings.HasPrefix(second, "PROTOTYPE-PAGE-ORDERS-") {
		t.Fatalf("prototype keys are not stable PageSpec-scoped identities: %q %q", first, second)
	}
}

func desiredTargetInput(
	t *testing.T,
	initializer CoreTargetArtifactInitializer,
	execution Execution,
	jobType string,
	sources []core.ManifestSourceInput,
) core.CreateArtifactInput {
	t.Helper()
	kind, key, title, content, ok := targetArtifactTemplate(execution, jobType)
	if !ok {
		t.Fatalf("job %s has no target template", jobType)
	}
	input, err := initializer.targetArtifactInput(context.Background(), execution, kind, key, title, content, sources)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func targetIterationRef(artifactID, label string) core.VersionRef {
	ref := toCoreVersionRef(platformRef(label))
	ref.ArtifactID = artifactID
	return ref
}

func TestCoreTargetArtifactInitializerReusesStableArtifactsAcrossSourceRevisions(t *testing.T) {
	t.Run("Baseline r1 to r2 reuses Blueprint", func(t *testing.T) {
		execution, _ := adapterExecution(t)
		artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
		baselineID := uuid.NewString()
		baselineR1 := targetIterationRef(baselineID, "baseline-r1")
		baselineR2 := targetIterationRef(baselineID, "baseline-r2")
		artifacts.addSource(baselineR1, "requirement_baseline")
		initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}
		firstSources := []core.ManifestSourceInput{{Ref: baselineR1, Purpose: "requirement_baseline"}}
		first, err := initializer.EnsureTarget(context.Background(), execution, "decompose_pages", firstSources)
		if err != nil {
			t.Fatal(err)
		}

		artifacts.addSource(baselineR2, "requirement_baseline")
		secondSources := []core.ManifestSourceInput{{Ref: baselineR2, Purpose: "requirement_baseline"}}
		second, err := initializer.EnsureTarget(context.Background(), execution, "decompose_pages", secondSources)
		if err != nil {
			t.Fatalf("second Blueprint iteration rejected new Baseline revision: %v", err)
		}
		if first == nil || second == nil || !sameCoreVersionRef(*first, *second) || len(artifacts.createdInputs) != 1 || artifacts.revisionCalls != 1 {
			t.Fatalf("Blueprint stable artifact/base was not reused: first=%+v second=%+v creates=%d revisions=%d", first, second, len(artifacts.createdInputs), artifacts.revisionCalls)
		}
		desired := desiredTargetInput(t, initializer, execution, "decompose_pages", secondSources)
		next := artifacts.applyAndCheckpoint(t, first.ArtifactID, desired)
		if next.ParentRevisionID == nil || *next.ParentRevisionID != first.RevisionID || len(next.SourceVersions) != 1 || next.SourceVersions[0].RevisionID != baselineR2.RevisionID {
			t.Fatalf("second Blueprint checkpoint lost new Baseline lineage: %+v", next)
		}
		third, err := initializer.EnsureTarget(context.Background(), execution, "decompose_pages", secondSources)
		if err != nil || third == nil || third.ArtifactID != first.ArtifactID || third.RevisionID != next.ID {
			t.Fatalf("new Blueprint revision did not become the next immutable proposal base: base=%+v err=%v", third, err)
		}
	})

	t.Run("Blueprint r1 to r2 on same page reuses PageSpec", func(t *testing.T) {
		execution, _ := adapterExecution(t)
		execution.Node.Key = "page-spec-ai:slice-orders"
		execution.Node.SliceID = "slice-orders"
		execution.Run.Context.Nodes[execution.Node.Key] = NodeMetadata{DefinitionNodeID: "page-spec-ai", SliceID: "slice-orders"}
		execution.Run.Context.Slices["slice-orders"] = SliceContext{
			ID: "slice-orders", Key: "page.orders", Title: "Orders",
			Payload: json.RawMessage(`{"route":"/orders","userGoal":"Manage orders"}`),
		}
		artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
		blueprintID := uuid.NewString()
		blueprintR1 := targetIterationRef(blueprintID, "blueprint-r1")
		blueprintR2 := targetIterationRef(blueprintID, "blueprint-r2")
		pageAnchor := "page-orders"
		blueprintR1.AnchorID, blueprintR2.AnchorID = &pageAnchor, &pageAnchor
		artifacts.addSource(blueprintR1, "blueprint")
		initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}
		firstSources := []core.ManifestSourceInput{{Ref: blueprintR1, Purpose: "delivery_slice_blueprint"}}
		first, err := initializer.EnsureTarget(context.Background(), execution, "generate_page_spec", firstSources)
		if err != nil {
			t.Fatal(err)
		}
		firstKey := artifacts.createdInputs[0].ArtifactKey
		if !strings.HasSuffix(firstKey, stableArtifactIdentitySuffix(blueprintID)+"-"+stableArtifactIdentitySuffix(pageAnchor)) {
			t.Fatalf("PageSpec key does not include stable Blueprint/page identity: %s", firstKey)
		}

		artifacts.addSource(blueprintR2, "blueprint")
		secondSources := []core.ManifestSourceInput{{Ref: blueprintR2, Purpose: "delivery_slice_blueprint"}}
		second, err := initializer.EnsureTarget(context.Background(), execution, "generate_page_spec", secondSources)
		if err != nil {
			t.Fatalf("second PageSpec iteration rejected the same Blueprint page at r2: %v", err)
		}
		if first == nil || second == nil || !sameCoreVersionRef(*first, *second) || len(artifacts.createdInputs) != 1 {
			t.Fatalf("PageSpec logical artifact was not reused: first=%+v second=%+v creates=%d", first, second, len(artifacts.createdInputs))
		}
		desired := desiredTargetInput(t, initializer, execution, "generate_page_spec", secondSources)
		if desired.ArtifactKey != firstKey {
			t.Fatalf("PageSpec key changed across Blueprint revisions: %s != %s", desired.ArtifactKey, firstKey)
		}
		next := artifacts.applyAndCheckpoint(t, first.ArtifactID, desired)
		if next.ParentRevisionID == nil || *next.ParentRevisionID != first.RevisionID || next.SourceVersions[0].RevisionID != blueprintR2.RevisionID {
			t.Fatalf("second PageSpec checkpoint lost Blueprint r2 lineage: %+v", next)
		}
	})

	t.Run("PageSpec r1 to r2 reuses Prototype", func(t *testing.T) {
		execution, _ := adapterExecution(t)
		execution.Node.Key = "prototype-ai:slice-orders"
		execution.Node.SliceID = "slice-orders"
		execution.Run.Context.Nodes[execution.Node.Key] = NodeMetadata{DefinitionNodeID: "prototype-ai", SliceID: "slice-orders"}
		execution.Run.Context.Slices["slice-orders"] = SliceContext{ID: "slice-orders", Key: "page.orders", Title: "Orders"}
		artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
		pageSpecID := uuid.NewString()
		pageSpecR1 := targetIterationRef(pageSpecID, "page-spec-r1")
		pageSpecR2 := targetIterationRef(pageSpecID, "page-spec-r2")
		artifacts.addSource(pageSpecR1, "page_spec")
		initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}
		firstSources := []core.ManifestSourceInput{{Ref: pageSpecR1, Purpose: "delivery_slice_page_spec"}}
		first, err := initializer.EnsureTarget(context.Background(), execution, "generate_prototype", firstSources)
		if err != nil {
			t.Fatal(err)
		}

		artifacts.addSource(pageSpecR2, "page_spec")
		secondSources := []core.ManifestSourceInput{{Ref: pageSpecR2, Purpose: "delivery_slice_page_spec"}}
		second, err := initializer.EnsureTarget(context.Background(), execution, "generate_prototype", secondSources)
		if err != nil {
			t.Fatalf("second Prototype iteration rejected PageSpec r2: %v", err)
		}
		if first == nil || second == nil || !sameCoreVersionRef(*first, *second) || len(artifacts.createdInputs) != 1 {
			t.Fatalf("Prototype logical artifact was not reused: first=%+v second=%+v creates=%d", first, second, len(artifacts.createdInputs))
		}
		desired := desiredTargetInput(t, initializer, execution, "generate_prototype", secondSources)
		next := artifacts.applyAndCheckpoint(t, first.ArtifactID, desired)
		var content struct {
			PageSpecRevision core.VersionRef `json:"pageSpecRevision"`
		}
		if next.ParentRevisionID == nil || *next.ParentRevisionID != first.RevisionID || json.Unmarshal(next.Content, &content) != nil ||
			!sameCoreVersionRef(content.PageSpecRevision, pageSpecR2) || next.SourceVersions[0].RevisionID != pageSpecR2.RevisionID {
			t.Fatalf("second Prototype checkpoint lost PageSpec r2 lineage: %+v content=%s", next, next.Content)
		}
	})
}

func TestCoreTargetArtifactInitializerRejectsDirtyReusableDraft(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		mutate func(*core.VersionedArtifact, core.VersionRef)
	}{
		{
			name: "content hash mismatch",
			mutate: func(value *core.VersionedArtifact, _ core.VersionRef) {
				value.Draft.Content = json.RawMessage(`{"nodes":[{"id":"uncheckpointed"}]}`)
				value.Draft.ContentHash = platformHash("uncheckpointed-draft")
			},
		},
		{
			name: "source-only dirty",
			mutate: func(value *core.VersionedArtifact, next core.VersionRef) {
				value.Draft.SourceVersions[0].VersionRef = next
			},
		},
		{
			name: "schema-only dirty",
			mutate: func(value *core.VersionedArtifact, _ core.VersionRef) {
				value.Draft.SchemaVersion++
			},
		},
		{
			name: "base mismatch",
			mutate: func(value *core.VersionedArtifact, _ core.VersionRef) {
				mismatch := uuid.NewString()
				value.Draft.BaseRevisionID = &mismatch
			},
		},
		{
			name: "status mismatch",
			mutate: func(value *core.VersionedArtifact, _ core.VersionRef) {
				value.Draft.Status = "frozen"
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			execution, _ := adapterExecution(t)
			artifacts := newFakeCoreTargetArtifacts(execution.Run.ProjectID)
			baselineID := uuid.NewString()
			baselineR1 := targetIterationRef(baselineID, "dirty-baseline-r1")
			baselineR2 := targetIterationRef(baselineID, "dirty-baseline-r2")
			artifacts.addSource(baselineR1, "requirement_baseline")
			initializer := CoreTargetArtifactInitializer{Artifacts: artifacts}
			first, err := initializer.EnsureTarget(context.Background(), execution, "decompose_pages", []core.ManifestSourceInput{{Ref: baselineR1}})
			if err != nil {
				t.Fatal(err)
			}
			value := artifacts.artifacts[first.ArtifactID]
			testCase.mutate(&value, baselineR2)
			artifacts.artifacts[first.ArtifactID] = value
			artifacts.addSource(baselineR2, "requirement_baseline")
			if _, err := initializer.EnsureTarget(context.Background(), execution, "decompose_pages", []core.ManifestSourceInput{{Ref: baselineR2}}); err == nil || !strings.Contains(err.Error(), "uncheckpointed draft") {
				t.Fatalf("dirty draft did not block target reuse: %v", err)
			}
			if len(artifacts.createdInputs) != 1 || artifacts.revisionCalls != 1 {
				t.Fatalf("dirty draft failure mutated target state: creates=%d revisions=%d", len(artifacts.createdInputs), artifacts.revisionCalls)
			}
		})
	}
}

func TestReusableTargetLineageRejectsLogicalIdentityDrift(t *testing.T) {
	t.Parallel()
	blueprintID := uuid.NewString()
	pageAnchor, otherAnchor := "page-orders", "page-other"
	blueprintR1 := targetIterationRef(blueprintID, "logical-blueprint-r1")
	blueprintR2 := targetIterationRef(blueprintID, "logical-blueprint-r2")
	blueprintR1.AnchorID, blueprintR2.AnchorID = &pageAnchor, &pageAnchor
	desiredPageSpec := core.CreateArtifactInput{
		Kind: "page_spec", ArtifactKey: "PAGE-SPEC-ORDERS",
		Content:        json.RawMessage(`{"blueprintPageNodeId":"page-orders"}`),
		SourceVersions: []core.ArtifactSourceInput{{Ref: blueprintR2, Purpose: "blueprint", Required: true}},
	}
	driftedBlueprint := blueprintR1
	driftedBlueprint.AnchorID = &otherAnchor
	if err := validateReusableTargetLineage("page_spec", json.RawMessage(`{"blueprintPageNodeId":"page-orders"}`), []core.ArtifactSource{{
		VersionRef: driftedBlueprint, Purpose: "blueprint", Required: true,
	}}, desiredPageSpec); err == nil {
		t.Fatal("PageSpec logical identity accepted a different Blueprint page anchor")
	}

	pageSpecA, pageSpecB := uuid.NewString(), uuid.NewString()
	pageSpecR1 := targetIterationRef(pageSpecA, "logical-page-spec-r1")
	pageSpecR2 := targetIterationRef(pageSpecA, "logical-page-spec-r2")
	otherPageSpec := targetIterationRef(pageSpecB, "logical-page-spec-other")
	desiredPrototypeContent, _ := domain.CanonicalJSON(map[string]any{"pageSpecRevision": pageSpecR2, "exploratory": false})
	actualPrototypeContent, _ := domain.CanonicalJSON(map[string]any{"pageSpecRevision": otherPageSpec, "exploratory": false})
	desiredPrototype := core.CreateArtifactInput{
		Kind: "prototype", ArtifactKey: "PROTOTYPE-ORDERS", Content: desiredPrototypeContent,
		SourceVersions: []core.ArtifactSourceInput{{Ref: pageSpecR2, Purpose: "page_spec", Required: true}},
	}
	if err := validateReusableTargetLineage("prototype", actualPrototypeContent, []core.ArtifactSource{{
		VersionRef: pageSpecR1, Purpose: "page_spec", Required: true,
	}}, desiredPrototype); err == nil {
		t.Fatal("Prototype logical identity accepted content for another PageSpec artifact")
	}
}

type fakeWorkbench struct {
	calls      int
	prototypes []core.VersionRef
	inputs     []core.CreateWorkbenchBundleInput
	states     map[string]core.WorkbenchLineageState
}

func (f *fakeWorkbench) CreateBundle(_ context.Context, projectID, actorID string, input core.CreateWorkbenchBundleInput) (core.WorkbenchBundle, error) {
	f.calls++
	prototype := input.PrototypeRevision
	f.prototypes = append(f.prototypes, prototype)
	f.inputs = append(f.inputs, input)
	return core.WorkbenchBundle{ID: uuid.NewString(), ProjectID: projectID, PrototypeRevision: prototype, PageSpecRevision: toCoreVersionRef(platformRef("page")), BlueprintRevision: toCoreVersionRef(platformRef("blueprint")), RequirementRevisions: []core.VersionRef{toCoreVersionRef(platformRef("requirements"))}, CreatedBy: actorID, CreatedAt: time.Now()}, nil
}

func (f *fakeWorkbench) GetLineageState(_ context.Context, rootID, _ string) (core.WorkbenchLineageState, error) {
	if state, exists := f.states[rootID]; exists {
		return state, nil
	}
	return core.WorkbenchLineageState{
		RootBundleID: rootID,
		ActiveBundle: core.WorkbenchBundle{ID: rootID, RootBuildManifestID: rootID},
	}, nil
}

func TestWorkbenchHookCreatesPerSliceBundlesAndFrozenBuildManifest(t *testing.T) {
	execution, _ := adapterExecution(t)
	execution.Definition = domain.NodeDefinition{ID: "compile", Name: "Compile", Type: domain.NodeManifestCompiler, InputSchema: engineSchema(), OutputSchema: engineSchema(), ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}}
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
	for ordinal, input := range service.inputs {
		if input.ManifestGroupKey == nil || *input.ManifestGroupKey != execution.Node.ID || input.RootOrdinal == nil || *input.RootOrdinal != ordinal {
			t.Fatalf("bundle %d lost stable compiler group/root order: %#v", ordinal, input)
		}
	}
	if manifest.ManifestGroupKey != execution.Node.ID {
		t.Fatalf("frozen BuildManifest group = %q, want node run %q", manifest.ManifestGroupKey, execution.Node.ID)
	}
}

func TestWorkbenchHookKeepsSelectionGroupsIsolatedAndFailsClosedBeforeWrites(t *testing.T) {
	execution, _ := adapterExecution(t)
	execution.Definition = domain.NodeDefinition{ID: "compile", Name: "Compile", Type: domain.NodeManifestCompiler, InputSchema: engineSchema(), OutputSchema: engineSchema(), ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}}
	blueprint := platformRef("selection-compiler-blueprint")
	pageSpecA, prototypeA := platformRef("selection-compiler-page-a"), platformRef("selection-compiler-prototype-a")
	pageSpecB, prototypeB := platformRef("selection-compiler-page-b"), platformRef("selection-compiler-prototype-b")
	manifestA := workflowSelectionManifest(t, execution.Run.ProjectID, execution.Run.StartedBy, blueprint, "page-a", pageSpecA, prototypeA)
	manifestB := workflowSelectionManifest(t, execution.Run.ProjectID, execution.Run.StartedBy, blueprint, "page-b", pageSpecB, prototypeB)
	service := &fakeWorkbench{}

	compile := func(manifest domain.InputManifest, nodeID string, pageSpec, prototype domain.ArtifactRef) BuildManifest {
		anchored := blueprint
		anchored.AnchorID = nodeID
		slice := SliceContext{ID: uuid.NewString(), Key: nodeID, Title: nodeID, FanOutNodeID: "pages", Blueprint: anchored, PageSpec: &pageSpec, Prototype: &prototype}
		current := execution
		current.Run.InputManifest = ptrManifest(manifest.Ref())
		current.Run.Context.Slices = map[string]SliceContext{slice.ID: slice}
		current.Node.ID = uuid.NewString()
		current.Inputs = adapterInputs(t, current, json.RawMessage(`{}`), slice)
		compiled, err := (CoreWorkbenchManifestHook{Workbench: service, Proposals: &fakeCoreProposals{manifest: manifest}}).Compile(context.Background(), current)
		if err != nil {
			t.Fatalf("compile %s selection: %v", nodeID, err)
		}
		return compiled
	}

	compiledA := compile(manifestA, "page-a", pageSpecA, prototypeA)
	compiledB := compile(manifestB, "page-b", pageSpecB, prototypeB)
	if compiledA.ManifestGroupKey == compiledB.ManifestGroupKey || len(compiledA.SliceIDs) != 1 || len(compiledB.SliceIDs) != 1 || service.calls != 2 {
		t.Fatalf("selection compiler groups collapsed: A=%#v B=%#v calls=%d", compiledA, compiledB, service.calls)
	}
	if service.prototypes[0].RevisionID != prototypeA.RevisionID || service.prototypes[1].RevisionID != prototypeB.RevisionID {
		t.Fatalf("selection compiler groups crossed prototype inputs: %#v", service.prototypes)
	}

	missingPrototype := workflowSelectionManifestMissingPrototype(t, execution.Run.ProjectID, execution.Run.StartedBy, blueprint, "page-a", pageSpecA)
	anchored := blueprint
	anchored.AnchorID = "page-a"
	badSlice := SliceContext{ID: uuid.NewString(), Key: "page-a", Title: "Page A", FanOutNodeID: "pages", Blueprint: anchored, PageSpec: &pageSpecA}
	execution.Run.InputManifest = ptrManifest(missingPrototype.Ref())
	execution.Run.Context.Slices = map[string]SliceContext{badSlice.ID: badSlice}
	execution.Node.ID = uuid.NewString()
	execution.Inputs = adapterInputs(t, execution, json.RawMessage(`{}`), badSlice)
	if _, err := (CoreWorkbenchManifestHook{Workbench: service, Proposals: &fakeCoreProposals{manifest: missingPrototype}}).Compile(context.Background(), execution); err == nil {
		t.Fatal("selection without an exact approved Prototype reached Workbench bundle creation")
	}
	if service.calls != 2 {
		t.Fatalf("fail-closed selection wrote a Workbench bundle; calls=%d", service.calls)
	}
}

func TestValidateBlueprintSelectionSlicesFailsClosed(t *testing.T) {
	blueprint, pageSpec, prototype := platformRef("selection-scope-blueprint"), platformRef("selection-scope-page-spec"), platformRef("selection-scope-prototype")
	anchored := blueprint
	anchored.AnchorID = "page-a"
	validScope := workflowBlueprintSelectionScope{
		SchemaVersion: 1, SelectionID: platformHash("selection-scope"), Blueprint: blueprint,
		NodeIDs:      []string{"page-a"},
		PageBindings: []workflowBlueprintSelectionPageBinding{{NodeID: "page-a", PageSpec: &pageSpec, Prototype: &prototype}},
	}
	validSlice := SliceContext{ID: uuid.NewString(), Key: "page-a", Blueprint: anchored, PageSpec: &pageSpec, Prototype: &prototype}
	if err := validateBlueprintSelectionSlices(validScope, []SliceContext{validSlice}); err != nil {
		t.Fatalf("valid selection slices failed: %v", err)
	}

	missingPageSpec := validScope
	missingPageSpec.PageBindings = []workflowBlueprintSelectionPageBinding{{NodeID: "page-a", Prototype: &prototype}}
	missingPrototype := validScope
	missingPrototype.PageBindings = []workflowBlueprintSelectionPageBinding{{NodeID: "page-a", PageSpec: &pageSpec}}
	crossPrototype := validSlice
	otherPrototype := platformRef("other-selection-prototype")
	crossPrototype.Prototype = &otherPrototype
	extraSlice := validSlice
	extraSlice.ID = uuid.NewString()
	extraSlice.Key = "page-outside"
	extraSlice.Blueprint.AnchorID = "page-outside"

	for _, testCase := range []struct {
		name   string
		scope  workflowBlueprintSelectionScope
		slices []SliceContext
	}{
		{name: "missing PageSpec", scope: missingPageSpec, slices: []SliceContext{validSlice}},
		{name: "missing Prototype", scope: missingPrototype, slices: []SliceContext{validSlice}},
		{name: "extra slice", scope: validScope, slices: []SliceContext{validSlice, extraSlice}},
		{name: "cross selection Prototype", scope: validScope, slices: []SliceContext{crossPrototype}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := validateBlueprintSelectionSlices(testCase.scope, testCase.slices); err == nil {
				t.Fatal("invalid selection slices reached Workbench compilation")
			}
		})
	}
}

func workflowSelectionManifestMissingPrototype(
	t *testing.T,
	projectID, actorID string,
	blueprint domain.ArtifactRef,
	nodeID string,
	pageSpec domain.ArtifactRef,
) domain.InputManifest {
	t.Helper()
	selectionID := platformHash(blueprint.RevisionID + "\x00missing\x00" + nodeID)
	anchored := blueprint
	anchored.AnchorID = nodeID
	scope := workflowBlueprintSelectionScope{
		SchemaVersion: 1, SelectionID: selectionID, Blueprint: blueprint, NodeIDs: []string{nodeID},
		PageBindings: []workflowBlueprintSelectionPageBinding{{NodeID: nodeID, PageSpec: &pageSpec}},
	}
	constraints, err := domain.CanonicalJSON(map[string]any{"blueprintSelection": scope})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, core.BlueprintSelectionJobType, selectionID, nil,
		[]domain.ManifestSource{{Ref: blueprint, Purpose: "blueprint_selection_root"}, {Ref: anchored, Purpose: "blueprint_selection_node"}, {Ref: pageSpec, Purpose: "selected_page_spec"}},
		constraints, "blueprint-selection/v1", actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
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
	manifest := BuildManifest{SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID, ManifestGroupKey: uuid.NewString(), SliceIDs: []string{uuid.NewString(), uuid.NewString()}, BundleIDs: []string{uuid.NewString(), uuid.NewString()}, Sources: []domain.ArtifactRef{platformRef("source")}, CreatedAt: time.Now()}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	execution.Inputs = adapterInputs(t, execution, mustJSON(manifest))
	generator := &fakeImplementationGenerator{}
	result, err := (GenerationWorkbenchRunner{Generation: generator, Workbench: &fakeWorkbench{}, DefaultModel: "model"}).Run(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 1 || result.Disposition != ResultWaitInput || len(result.Output) == 0 {
		t.Fatalf("unexpected workbench result: %+v", result)
	}
	var output struct {
		ImplementationProposals []struct {
			BundleID string `json:"bundleId"`
		} `json:"implementationProposals"`
		PendingBundleIDs []string `json:"pendingBundleIds"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.ImplementationProposals) != 1 || output.ImplementationProposals[0].BundleID != manifest.BundleIDs[0] ||
		len(output.PendingBundleIDs) != 1 || output.PendingBundleIDs[0] != manifest.BundleIDs[1] {
		t.Fatalf("runner did not hold later bundles for exact rebase: %+v", output)
	}
}

func TestWorkbenchRunnerWaitsForRebaseWithoutCallingAI(t *testing.T) {
	t.Parallel()

	execution, _ := adapterExecution(t)
	rootID, laterRootID := uuid.NewString(), uuid.NewString()
	manifest := BuildManifest{
		SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID,
		ManifestGroupKey: uuid.NewString(), SliceIDs: []string{uuid.NewString(), uuid.NewString()},
		BundleIDs: []string{rootID, laterRootID}, Sources: []domain.ArtifactRef{platformRef("source")},
		CreatedAt: time.Now(),
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	execution.Inputs = adapterInputs(t, execution, mustJSON(manifest))
	oldWorkspace := toCoreVersionRef(platformRef("workspace-old"))
	currentWorkspace := toCoreVersionRef(platformRef("workspace-current"))
	workbench := &fakeWorkbench{states: map[string]core.WorkbenchLineageState{
		rootID: {
			RootBundleID: rootID,
			ActiveBundle: core.WorkbenchBundle{
				ID: rootID, RootBuildManifestID: rootID, CurrentWorkspaceRevision: &oldWorkspace,
			},
			CurrentWorkspaceRevision: &currentWorkspace,
		},
	}}
	generator := &fakeImplementationGenerator{}
	result, err := (GenerationWorkbenchRunner{
		Generation: generator, Workbench: workbench, DefaultModel: "model",
	}).Run(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 0 || result.Disposition != ResultWaitInput {
		t.Fatalf("runner called AI before rebase: calls=%d result=%+v", generator.calls, result)
	}
	var output struct {
		ImplementationProposals []map[string]any `json:"implementationProposals"`
		PendingBundleIDs        []string         `json:"pendingBundleIds"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.ImplementationProposals) != 0 || !reflect.DeepEqual(output.PendingBundleIDs, manifest.BundleIDs) {
		t.Fatalf("rebase wait payload = %#v", output)
	}
}

func TestWorkbenchRunnerRecoversDerivedActiveProposalWithoutCallingAI(t *testing.T) {
	t.Parallel()

	execution, _ := adapterExecution(t)
	rootID, derivedID := uuid.NewString(), uuid.NewString()
	manifest := BuildManifest{
		SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID,
		ManifestGroupKey: uuid.NewString(), SliceIDs: []string{uuid.NewString()}, BundleIDs: []string{rootID},
		Sources: []domain.ArtifactRef{platformRef("source")}, CreatedAt: time.Now(),
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	execution.Inputs = adapterInputs(t, execution, mustJSON(manifest))
	workspace := toCoreVersionRef(platformRef("workspace"))
	proposal := core.ImplementationProposal{
		ID: uuid.NewString(), BuildManifestID: derivedID, Status: "ready", PayloadHash: platformHash("proposal"),
	}
	workbench := &fakeWorkbench{states: map[string]core.WorkbenchLineageState{
		rootID: {
			RootBundleID: rootID,
			ActiveBundle: core.WorkbenchBundle{
				ID: derivedID, RootBuildManifestID: rootID, CurrentWorkspaceRevision: &workspace,
			},
			CurrentWorkspaceRevision: &workspace, CurrentProposal: &proposal,
		},
	}}
	generator := &fakeImplementationGenerator{}
	result, err := (GenerationWorkbenchRunner{
		Generation: generator, Workbench: workbench, DefaultModel: "model",
	}).Run(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 0 {
		t.Fatal("runner regenerated an existing active derived proposal")
	}
	var output struct {
		ImplementationProposals []struct {
			BundleID       string `json:"bundleId"`
			ActiveBundleID string `json:"activeBundleId"`
			ProposalID     string `json:"proposalId"`
		} `json:"implementationProposals"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.ImplementationProposals) != 1 || output.ImplementationProposals[0].BundleID != rootID ||
		output.ImplementationProposals[0].ActiveBundleID != derivedID || output.ImplementationProposals[0].ProposalID != proposal.ID {
		t.Fatalf("derived proposal recovery payload = %#v", output)
	}
}

func TestBuildManifestRootLineageRejectsAnotherWorkflowRun(t *testing.T) {
	t.Parallel()

	rootID := uuid.New()
	projectID := uuid.New()
	actualRunID := uuid.New()
	otherRunID := uuid.New()
	manifestGroupKey := uuid.NewString()
	manifest := storage.ApplicationBuildManifestModel{
		ID: rootID, ProjectID: projectID, RootManifestID: rootID, WorkflowRunID: &actualRunID,
		ManifestGroupKey: &manifestGroupKey,
	}
	if err := validateBuildManifestRootLineage(
		context.Background(), nil, manifest, rootID, projectID, otherRunID,
	); err == nil {
		t.Fatal("same-project manifest from another workflow run was accepted")
	}
	manifest.WorkflowRunID = nil
	if err := validateBuildManifestRootLineage(
		context.Background(), nil, manifest, rootID, projectID, actualRunID,
	); err == nil {
		t.Fatal("manifest without an exact workflow run was accepted")
	}
	manifest.WorkflowRunID = &actualRunID
	if err := validateBuildManifestRootLineage(
		context.Background(), nil, manifest, rootID, projectID, actualRunID,
	); err != nil {
		t.Fatalf("exact root workflow lineage was rejected: %v", err)
	}
}

func TestWorkbenchRunnerConsumesReviewedConversationInstructionFromScope(t *testing.T) {
	execution, _ := adapterExecution(t)
	manifest := BuildManifest{SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID, ManifestGroupKey: uuid.NewString(), SliceIDs: []string{uuid.NewString()}, BundleIDs: []string{uuid.NewString()}, Sources: []domain.ArtifactRef{platformRef("source")}, CreatedAt: time.Now()}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	execution.Inputs = adapterInputs(t, execution, mustJSON(manifest))
	execution.Run.Scope = json.RawMessage(`{"conversationIntent":{"workbenchInstruction":{"objective":"Build the approved dashboard","constraints":["Use exact API contracts"]}}}`)
	generator := &fakeImplementationGenerator{}
	if _, err := (GenerationWorkbenchRunner{Generation: generator, Workbench: &fakeWorkbench{}, DefaultModel: "model", Instruction: "fallback"}).Run(context.Background(), execution); err != nil {
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
		ManifestGroupKey: uuid.NewString(),
		SliceIDs:         []string{"selected-slice"}, BundleIDs: []string{"selected-bundle"},
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

func TestSelectedImplementationProposalOrderMatchesWorkspaceAncestry(t *testing.T) {
	t.Parallel()

	first, second, third := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := validateSelectedProposalOrder(
		[]string{first, second, third}, []string{third, second, first},
	); err != nil {
		t.Fatalf("valid root-to-leaf apply order rejected: %v", err)
	}
	if err := validateSelectedProposalOrder(
		[]string{first, second, third}, []string{second, third, first},
	); err == nil {
		t.Fatal("workspace ancestry accepted proposals applied outside frozen root order")
	}
	if err := validateSelectedProposalOrder(
		[]string{first, second, third}, []string{third, first},
	); err == nil {
		t.Fatal("workspace ancestry accepted a missing selected proposal")
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
