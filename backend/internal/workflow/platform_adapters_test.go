package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func adapterInputs(t *testing.T, execution Execution, output json.RawMessage, slices ...SliceContext) domain.NodeInputEnvelope {
	t.Helper()
	refs := make([]domain.WorkflowSliceRef, 0, len(slices))
	for _, slice := range slices {
		refs = append(refs, domain.WorkflowSliceRef{ID: slice.ID, Key: slice.Key, FanOutNodeID: slice.FanOutNodeID})
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

func TestCoreManifestFreezerPreservesExactVersionPins(t *testing.T) {
	execution, upstream := adapterExecution(t)
	proposals := &fakeCoreProposals{manifest: upstream}
	frozen, err := (CoreManifestFreezer{Proposals: proposals}).Freeze(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Sources[0].Ref != upstream.Sources[0].Ref || frozen.BaseRevision == nil || !frozen.BaseRevision.Equal(*upstream.BaseRevision) {
		t.Fatalf("manifest refs were not preserved: %+v", frozen)
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

type fakeWorkbench struct{ calls int }

func (f *fakeWorkbench) CreateBundle(_ context.Context, projectID, actorID string, input core.CreateWorkbenchBundleInput) (core.WorkbenchBundle, error) {
	f.calls++
	prototype := input.PrototypeRevision
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

type fakeImplementationGenerator struct{ calls int }

func (f *fakeImplementationGenerator) GenerateImplementation(_ context.Context, bundleID, actorID, model, instruction string) (generation.ImplementationGenerationResult, error) {
	f.calls++
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
