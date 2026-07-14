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

func TestExecutionReceivesMappedImmutablePortInput(t *testing.T) {
	now := time.Now().UTC()
	userID := uuid.NewString()
	sourceSchema := json.RawMessage(`{"type":"object","properties":{"payload":{"type":"object"}},"required":["payload"]}`)
	targetSchema := json.RawMessage(`{"type":"object","properties":{"title":{"type":"object"}},"required":["title"]}`)
	openSchema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	nodes := []domain.NodeDefinition{
		{ID: "source", Name: "Source", Type: domain.NodeArtifactInput, InputSchema: sourceSchema, OutputPorts: map[string]domain.PortDefinition{"artifact": {Schema: sourceSchema}}, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}},
		{ID: "target", Name: "Target", Type: domain.NodeTransform, InputPorts: map[string]domain.PortDefinition{"request": {Schema: targetSchema}}, OutputSchema: openSchema, Transform: &domain.TransformNodeConfig{Transform: "mapped"}},
	}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Mapped", "2", nodes, []domain.WorkflowEdge{{ID: "mapped-edge", From: "source", FromPort: "artifact", To: "target", ToPort: "request", Mapping: map[string]string{"title": "payload"}}}, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewMapRegistry()
	var received domain.NodeInputEnvelope
	if err := registry.Register(domain.NodeTransform, RunnerFunc(func(_ context.Context, execution Execution) (WorkerResult, error) {
		received = execution.Inputs
		return WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"ok":true}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	engine, _, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	if _, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	value, source, ok := received.FirstValue("request")
	if !ok || source.DefinitionNodeID != "source" || !domain.IsCanonicalHash(received.Hash()) {
		t.Fatalf("missing pinned mapped input: source=%+v hash=%q", source, received.Hash())
	}
	var mapped map[string]json.RawMessage
	if err := json.Unmarshal(value, &mapped); err != nil {
		t.Fatal(err)
	}
	if len(mapped["title"]) == 0 || len(mapped["payload"]) == 0 {
		t.Fatalf("mapping did not materialize title from payload: %s", value)
	}
	bindings := received.Bindings()
	bindings[0].Value[0] = '['
	stable, _, _ := received.FirstValue("request")
	if string(stable) != string(value) {
		t.Fatalf("mutating a returned binding changed the immutable envelope: before=%s after=%s", value, stable)
	}
}

func TestRuntimeInputSchemaFailsClosedBeforeRunner(t *testing.T) {
	now := time.Now().UTC()
	userID := uuid.NewString()
	outputSchema := json.RawMessage(`{"type":"object","properties":{"payload":{"type":"object"}},"required":["payload"]}`)
	inputSchema := json.RawMessage(`{"type":"object","properties":{"payload":{"type":"object","properties":{"requiredValue":{"type":"string"}},"required":["requiredValue"]}},"required":["payload"]}`)
	nodes := []domain.NodeDefinition{
		{ID: "source", Name: "Source", Type: domain.NodeArtifactInput, InputSchema: outputSchema, OutputSchema: outputSchema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}},
		{ID: "target", Name: "Target", Type: domain.NodeTransform, InputSchema: inputSchema, OutputSchema: outputSchema, Transform: &domain.TransformNodeConfig{Transform: "must-not-run"}},
	}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Runtime schema", "2", nodes, []domain.WorkflowEdge{{ID: "edge", From: "source", To: "target"}}, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewMapRegistry()
	called := false
	if err := registry.Register(domain.NodeTransform, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		called = true
		return WorkerResult{Disposition: ResultComplete}, nil
	})); err != nil {
		t.Fatal(err)
	}
	engine, _, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	if _, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	err = engine.ClaimAndExecute(context.Background(), "worker")
	if err == nil || !strings.Contains(err.Error(), "requiredValue") {
		t.Fatalf("expected nested runtime input schema rejection, got %v", err)
	}
	if called {
		t.Fatal("runner was invoked after runtime input schema validation failed")
	}
}

func TestTwoFanOutRegionsKeepManifestInputsIsolated(t *testing.T) {
	now := time.Now().UTC()
	userID := uuid.NewString()
	schema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	transform := func(id string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Name: id, Type: domain.NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &domain.TransformNodeConfig{Transform: id}}
	}
	fan := func(id, merge string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Name: id, Type: domain.NodeFanOut, InputSchema: schema, OutputSchema: schema, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/items", SliceKeyPath: "/key", MergeNodeID: merge, MaxParallel: 2}}
	}
	merge := func(id, fanOut string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Name: id, Type: domain.NodeMerge, InputSchema: schema, OutputSchema: schema, Merge: &domain.MergeNodeConfig{FanOutNodeID: fanOut, Policy: domain.MergeAll}}
	}
	compile := func(id string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Name: id, Type: domain.NodeManifestCompiler, InputSchema: schema, OutputSchema: schema, ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}}
	}
	nodes := []domain.NodeDefinition{transform("entry"), fan("fan-a", "merge-a"), transform("work-a"), merge("merge-a", "fan-a"), compile("compile-a"), fan("fan-b", "merge-b"), transform("work-b"), merge("merge-b", "fan-b"), compile("compile-b"), transform("join")}
	edges := []domain.WorkflowEdge{
		{ID: "entry-a", From: "entry", To: "fan-a"}, {ID: "fan-a-work", From: "fan-a", To: "work-a"}, {ID: "work-a-merge", From: "work-a", To: "merge-a"}, {ID: "merge-a-compile", From: "merge-a", To: "compile-a"}, {ID: "compile-a-join", From: "compile-a", To: "join"},
		{ID: "entry-b", From: "entry", To: "fan-b"}, {ID: "fan-b-work", From: "fan-b", To: "work-b"}, {ID: "work-b-merge", From: "work-b", To: "merge-b"}, {ID: "merge-b-compile", From: "merge-b", To: "compile-b"}, {ID: "compile-b-join", From: "compile-b", To: "join"},
	}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Two fan-outs", "2", nodes, edges, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	definition.OutputContract = &domain.WorkflowOutputContract{
		Capability: domain.WorkflowOutputApplication, ProducedArtifactKinds: []string{"workspace"},
		TerminalOutcome: domain.WorkflowOutcomeApplication, TerminalNodeType: domain.NodeWorkbenchBuild,
	}
	run := syntheticRun(definition, userID)
	inputManifest, err := domain.NewInputManifest(
		uuid.NewString(), run.ProjectID, "workflow-test", "", nil,
		[]domain.ManifestSource{{Ref: platformRef("workflow-test-source"), Purpose: "workflow_input"}},
		json.RawMessage(`{"test":"fanout-isolation"}`), "workflow-test/v1", userID, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	run.InputManifest = ptrManifest(inputManifest.Ref())
	sliceA := syntheticSlice("a", "fan-a")
	sliceB := syntheticSlice("b", "fan-b")
	run.Context.Slices[sliceA.ID], run.Context.Slices[sliceB.ID] = sliceA, sliceB
	prepareFanOutRegion(t, run, definition, "fan-a", "work-a", "merge-a", sliceA)
	prepareFanOutRegion(t, run, definition, "fan-b", "work-b", "merge-b", sliceB)
	otherA := syntheticSlice("a-other", "fan-a")
	run.Context.Slices[otherA.ID] = otherA
	fanAMetadata := run.Context.Nodes["fan-a"]
	fanAMetadata.FanOutOutputs[otherA.ID] = mustJSON(FanOutItem{Key: otherA.Key, Title: otherA.Title, Blueprint: otherA.Blueprint, PageSpec: otherA.PageSpec, Prototype: otherA.Prototype})
	run.Context.Nodes["fan-a"] = fanAMetadata
	workA := run.Nodes[instanceKey("work-a", sliceA.ID)]
	isolationInputs, err := buildNodeInputEnvelope(run, definition, workA)
	if err != nil {
		t.Fatal(err)
	}
	isolated, _, ok := isolationInputs.FirstValue("default")
	if !ok || strings.Contains(string(isolated), otherA.Key) || !strings.Contains(string(isolated), sliceA.Key) {
		t.Fatalf("fan-out root input was not slice-isolated: %s", isolated)
	}

	compileA := addSyntheticNode(run, "compile-a", "compile-a", "", NodeRunning)
	inputsA, err := buildNodeInputEnvelope(run, definition, compileA)
	if err != nil {
		t.Fatal(err)
	}
	compileB := addSyntheticNode(run, "compile-b", "compile-b", "", NodeRunning)
	inputsB, err := buildNodeInputEnvelope(run, definition, compileB)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlySlice(t, inputsA.SliceRefs(), sliceA)
	assertOnlySlice(t, inputsB.SliceRefs(), sliceB)
	mutatedA := run.Context.Slices[sliceA.ID]
	mutatedPrototype := platformRef("mutated-prototype-a")
	mutatedA.Prototype = &mutatedPrototype
	run.Context.Slices[sliceA.ID] = mutatedA

	service := &fakeWorkbench{}
	definitionA, _ := definition.FindNode("compile-a")
	proposals := &fakeCoreProposals{manifest: inputManifest}
	manifestA, err := (CoreWorkbenchManifestHook{Workbench: service, Proposals: proposals}).Compile(context.Background(), Execution{Run: *run, Node: *compileA, Definition: definitionA, Workflow: definition, Inputs: inputsA})
	if err != nil {
		t.Fatal(err)
	}
	definitionB, _ := definition.FindNode("compile-b")
	manifestB, err := (CoreWorkbenchManifestHook{Workbench: service, Proposals: proposals}).Compile(context.Background(), Execution{Run: *run, Node: *compileB, Definition: definitionB, Workflow: definition, Inputs: inputsB})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifestA.SliceIDs) != 1 || manifestA.SliceIDs[0] != sliceA.ID || len(manifestB.SliceIDs) != 1 || manifestB.SliceIDs[0] != sliceB.ID || service.calls != 2 {
		t.Fatalf("fan-out manifests leaked slices: A=%v B=%v calls=%d", manifestA.SliceIDs, manifestB.SliceIDs, service.calls)
	}
	if len(service.prototypes) != 2 || service.prototypes[0].RevisionID != sliceA.Prototype.RevisionID {
		t.Fatalf("manifest compiler did not use the immutable incoming prototype pin: %+v", service.prototypes)
	}
}

func TestSerialFanOutEpochReplacesPriorSliceLineage(t *testing.T) {
	now := time.Now().UTC()
	userID := uuid.NewString()
	schema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	transform := func(id string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Name: id, Type: domain.NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &domain.TransformNodeConfig{Transform: id}}
	}
	// Explicit anchors prove epoch pruning removes every anchor variant of the
	// same immutable revision, including the whole Blueprint inherited before
	// the first fan-out and the page-anchored ref emitted by its slice.
	fan := func(id, merge string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Name: id, Type: domain.NodeFanOut, InputSchema: schema, OutputSchema: schema, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/items", SliceKeyPath: "/key", MergeNodeID: merge, MaxParallel: 2}}
	}
	merge := func(id, fanOut string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Name: id, Type: domain.NodeMerge, InputSchema: schema, OutputSchema: schema, Merge: &domain.MergeNodeConfig{FanOutNodeID: fanOut, Policy: domain.MergeAll}}
	}
	nodes := []domain.NodeDefinition{
		transform("entry"), fan("fan-a", "merge-a"), transform("work-a"), merge("merge-a", "fan-a"),
		fan("fan-b", "merge-b"), transform("work-b"), merge("merge-b", "fan-b"),
		{ID: "compiler", Name: "compiler", Type: domain.NodeManifestCompiler, InputSchema: schema, OutputSchema: schema, ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}},
	}
	edges := []domain.WorkflowEdge{
		{ID: "entry-a", From: "entry", To: "fan-a"}, {ID: "fan-a-work", From: "fan-a", To: "work-a"},
		{ID: "work-a-merge", From: "work-a", To: "merge-a"}, {ID: "merge-a-fan-b", From: "merge-a", To: "fan-b"},
		{ID: "fan-b-work", From: "fan-b", To: "work-b"}, {ID: "work-b-merge", From: "work-b", To: "merge-b"},
		{ID: "merge-b-compiler", From: "merge-b", To: "compiler"},
	}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Serial fan-outs", "2", nodes, edges, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	run := syntheticRun(definition, userID)
	entry := addSyntheticNode(run, "entry", "entry", "", NodeCompleted)
	run.Context.Nodes[entry.Key] = NodeMetadata{DefinitionNodeID: "entry", Output: json.RawMessage(`{"items":[]}`)}

	sliceA := syntheticSlice("old", "fan-a")
	wholeBlueprint := sliceA.Blueprint
	anchoredBlueprint := sliceA.Blueprint
	anchorID := "page-old"
	anchoredBlueprint.AnchorID = anchorID
	sliceA.Blueprint = anchoredBlueprint
	run.Context.Slices[sliceA.ID] = sliceA
	prepareFanOutRegion(t, run, definition, "fan-a", "work-a", "merge-a", sliceA)
	mergeAMetadata := run.Context.Nodes["merge-a"]
	var mergeAInputs domain.NodeInputEnvelope
	if err := json.Unmarshal(mergeAMetadata.Input, &mergeAInputs); err != nil {
		t.Fatal(err)
	}
	mergeABindings := mergeAInputs.Bindings()
	mergeABindings[0].Source.ArtifactRevisions = appendUniqueArtifactRef(mergeABindings[0].Source.ArtifactRevisions, wholeBlueprint)
	mergeAInputs, err = domain.NewNodeInputEnvelope(mergeABindings)
	if err != nil {
		t.Fatal(err)
	}
	mergeAMetadata.Input = mergeAInputs.Canonical()
	run.Context.Nodes["merge-a"] = mergeAMetadata

	sliceB := syntheticSlice("current", "fan-b")
	run.Context.Slices[sliceB.ID] = sliceB
	fanB := addSyntheticNode(run, "fan-b", "fan-b", "", NodeRunning)
	fanBInputs, err := buildNodeInputEnvelope(run, definition, fanB)
	if err != nil {
		t.Fatal(err)
	}
	fanB.Status = NodeCompleted
	run.Context.Nodes[fanB.Key] = NodeMetadata{
		DefinitionNodeID: "fan-b", Input: fanBInputs.Canonical(),
		FanOutOutputs: map[string]json.RawMessage{sliceB.ID: mustJSON(FanOutItem{Key: sliceB.Key, Title: sliceB.Title, Blueprint: sliceB.Blueprint, PageSpec: sliceB.PageSpec, Prototype: sliceB.Prototype})},
	}
	workB := addSyntheticNode(run, instanceKey("work-b", sliceB.ID), "work-b", sliceB.ID, NodeRunning)
	workBInputs, err := buildNodeInputEnvelope(run, definition, workB)
	if err != nil {
		t.Fatal(err)
	}
	workB.Status = NodeCompleted
	run.Context.Nodes[workB.Key] = NodeMetadata{DefinitionNodeID: "work-b", SliceID: sliceB.ID, Input: workBInputs.Canonical(), Output: json.RawMessage(`{"ok":true}`)}
	mergeB := addSyntheticNode(run, "merge-b", "merge-b", "", NodeRunning)
	mergeBInputs, err := buildNodeInputEnvelope(run, definition, mergeB)
	if err != nil {
		t.Fatal(err)
	}
	mergeB.Status = NodeCompleted
	run.Context.Nodes[mergeB.Key] = NodeMetadata{DefinitionNodeID: "merge-b", Input: mergeBInputs.Canonical()}
	compiler := addSyntheticNode(run, "compiler", "compiler", "", NodeRunning)
	compilerInputs, err := buildNodeInputEnvelope(run, definition, compiler)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlySlice(t, compilerInputs.SliceRefs(), sliceB)
	for _, stale := range []domain.ArtifactRef{wholeBlueprint, anchoredBlueprint, *sliceA.PageSpec, *sliceA.Prototype} {
		for _, ref := range compilerInputs.ArtifactRefs() {
			if sameArtifactRevision(ref, stale) {
				t.Fatalf("second fan-out compiler retained stale first-epoch artifact: %+v", ref)
			}
		}
	}
}

func TestHumanEditMaterializationPreventsReviewQuorumFromRevalidatingUpstreamArtifacts(t *testing.T) {
	upstream, current := platformRef("review-upstream-brief"), platformRef("review-current-requirements")
	stored, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "ai-human", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{
			RunID: uuid.NewString(), NodeKey: "requirements-ai", DefinitionNodeID: "requirements-ai",
			ArtifactRevisions: []domain.ArtifactRef{upstream},
		},
		Output: json.RawMessage(`{}`), Value: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := domain.CanonicalJSON(map[string]any{"artifactRevision": current})
	if err != nil {
		t.Fatal(err)
	}
	run := &RunRecord{ID: uuid.NewString(), Context: NewRunContext()}
	run.Context.Slices["slice-review"] = SliceContext{ID: "slice-review", Key: "review", FanOutNodeID: "pages", Blueprint: upstream}
	source := &NodeRecord{Key: "requirements-edit:slice-review", DefinitionNodeID: "requirements-edit", SliceID: "slice-review", Type: domain.NodeHumanEdit, Status: NodeCompleted}
	definition := domain.NodeDefinition{ID: "requirements-edit", Type: domain.NodeHumanEdit, HumanEdit: &domain.HumanEditNodeConfig{ArtifactKind: "product_requirements"}}
	reference := nodeOutputReference(run, definition, source, output, stored, true, "")
	if len(reference.ArtifactRevisions) != 2 {
		t.Fatalf("HumanEdit materialization lost complete generation lineage: %#v", reference.ArtifactRevisions)
	}
	if len(reference.MaterializedArtifactRevisions) != 1 || !reference.MaterializedArtifactRevisions[0].Equal(current) {
		t.Fatalf("HumanEdit did not isolate its current review materialization: %#v", reference.MaterializedArtifactRevisions)
	}
	if len(reference.DeliverySliceRefs) != 1 || reference.DeliverySliceRefs[0].ID != "slice-review" {
		t.Fatalf("HumanEdit materialization lost its non-reviewable slice lineage: %#v", reference.DeliverySliceRefs)
	}
	conditionInput, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "human-condition", FromPort: "default", ToPort: "default", Source: reference,
		Output: output, Value: output,
	}})
	if err != nil {
		t.Fatal(err)
	}
	conditionDefinition := domain.NodeDefinition{ID: "review-condition", Type: domain.NodeCondition, Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{{Name: "yes"}, {Name: "no"}}}}
	conditionNode := &NodeRecord{Key: "review-condition:slice-review", DefinitionNodeID: conditionDefinition.ID, SliceID: "slice-review", Type: domain.NodeCondition, Status: NodeCompleted}
	conditionReference := nodeOutputReference(run, conditionDefinition, conditionNode, output, conditionInput, true, "")
	if len(conditionReference.MaterializedArtifactRevisions) != 1 || !conditionReference.MaterializedArtifactRevisions[0].Equal(current) || len(conditionReference.ArtifactRevisions) != 2 {
		t.Fatalf("transparent control node did not preserve review marker and full generation context: %+v", conditionReference)
	}
}

func TestPrototypeHumanEditRefreshesSliceLineageThroughReviewMergeAndCompiler(t *testing.T) {
	now := time.Now().UTC()
	actorID := uuid.NewString()
	definition, err := MinimumLoopDefinition(uuid.NewString(), actorID, now)
	if err != nil {
		t.Fatal(err)
	}
	run := syntheticRun(definition, actorID)
	inputManifest, err := domain.NewInputManifest(
		uuid.NewString(), run.ProjectID, "workflow-test", "", nil,
		[]domain.ManifestSource{{Ref: platformRef("prototype-refresh-source"), Purpose: "workflow_input"}},
		json.RawMessage(`{"test":"prototype-refresh"}`), "workflow-test/v1", actorID, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	run.InputManifest = ptrManifest(inputManifest.Ref())

	slice := syntheticSlice("prototype-refresh", "pages")
	prototype := *slice.Prototype
	slice.Prototype = nil
	run.Context.Slices[slice.ID] = slice

	pageSpecReview := addSyntheticNode(run, instanceKey("page-spec-review", slice.ID), "page-spec-review", slice.ID, NodeCompleted)
	run.Context.Nodes[pageSpecReview.Key] = NodeMetadata{
		DefinitionNodeID: pageSpecReview.DefinitionNodeID,
		SliceID:          slice.ID,
		Output:           json.RawMessage(`{"payload":{"approved":true}}`),
	}

	prototypeAI := addSyntheticNode(run, instanceKey("prototype-ai", slice.ID), "prototype-ai", slice.ID, NodeRunning)
	prototypeAIInputs, err := buildNodeInputEnvelope(run, definition, prototypeAI)
	if err != nil {
		t.Fatal(err)
	}
	if refs := prototypeAIInputs.SliceRefs(); len(refs) != 1 || refs[0].Prototype != nil {
		t.Fatalf("Prototype generation must begin from a slice without a Prototype revision: %+v", refs)
	}
	prototypeAI.Status = NodeCompleted
	run.Context.Nodes[prototypeAI.Key] = NodeMetadata{
		DefinitionNodeID: prototypeAI.DefinitionNodeID,
		SliceID:          slice.ID,
		Input:            prototypeAIInputs.Canonical(),
		Output:           json.RawMessage(`{"payload":{"proposal":"ready"}}`),
	}

	prototypeEdit := addSyntheticNode(run, instanceKey("prototype-edit", slice.ID), "prototype-edit", slice.ID, NodeRunning)
	prototypeEditInputs, err := buildNodeInputEnvelope(run, definition, prototypeEdit)
	if err != nil {
		t.Fatal(err)
	}
	if refs := prototypeEditInputs.SliceRefs(); len(refs) != 1 || refs[0].Prototype != nil {
		t.Fatalf("HumanEdit input must retain its pre-materialization slice snapshot: %+v", refs)
	}
	prototypeEdit.Status = NodeCompleted
	run.Context.Nodes[prototypeEdit.Key] = NodeMetadata{
		DefinitionNodeID: prototypeEdit.DefinitionNodeID,
		SliceID:          slice.ID,
		Input:            prototypeEditInputs.Canonical(),
		Output:           mustJSON(map[string]any{"artifactRevision": prototype}),
	}
	if err := applyHumanEditSliceLineage(run, slice.ID, HumanEditValidation{
		ArtifactRefs: []domain.ArtifactRef{prototype}, Primary: prototype, ArtifactKind: "prototype",
	}); err != nil {
		t.Fatal(err)
	}

	prototypeReview := addSyntheticNode(run, instanceKey("prototype-review", slice.ID), "prototype-review", slice.ID, NodeRunning)
	prototypeReviewInputs, err := buildNodeInputEnvelope(run, definition, prototypeReview)
	if err != nil {
		t.Fatal(err)
	}
	assertSlicePrototype(t, prototypeReviewInputs.SliceRefs(), slice.ID, prototype)
	prototypeReview.Status = NodeCompleted
	run.Context.Nodes[prototypeReview.Key] = NodeMetadata{
		DefinitionNodeID: prototypeReview.DefinitionNodeID,
		SliceID:          slice.ID,
		Input:            prototypeReviewInputs.Canonical(),
	}

	merge := addSyntheticNode(run, "pages-merged", "pages-merged", "", NodeRunning)
	mergeInputs, err := buildNodeInputEnvelope(run, definition, merge)
	if err != nil {
		t.Fatal(err)
	}
	assertSlicePrototype(t, mergeInputs.SliceRefs(), slice.ID, prototype)
	merge.Status = NodeCompleted
	run.Context.Nodes[merge.Key] = NodeMetadata{DefinitionNodeID: merge.DefinitionNodeID, Input: mergeInputs.Canonical()}

	compiler := addSyntheticNode(run, "compile-manifest", "compile-manifest", "", NodeRunning)
	compilerInputs, err := buildNodeInputEnvelope(run, definition, compiler)
	if err != nil {
		t.Fatal(err)
	}
	assertSlicePrototype(t, compilerInputs.SliceRefs(), slice.ID, prototype)
	compilerDefinition, _ := definition.FindNode("compile-manifest")
	workbench := &fakeWorkbench{}
	manifest, err := (CoreWorkbenchManifestHook{
		Workbench: workbench, Proposals: &fakeCoreProposals{manifest: inputManifest},
	}).Compile(context.Background(), Execution{
		Run: *run, Node: *compiler, Definition: compilerDefinition, Workflow: definition, Inputs: compilerInputs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if workbench.calls != 1 || len(workbench.prototypes) != 1 || workbench.prototypes[0].RevisionID != prototype.RevisionID ||
		len(manifest.SliceIDs) != 1 || manifest.SliceIDs[0] != slice.ID {
		t.Fatalf("compiler did not consume the resumed immutable Prototype: manifest=%+v prototypes=%+v", manifest, workbench.prototypes)
	}
}

func TestCompilerRetryEnrichesCompletedMergeSliceLineage(t *testing.T) {
	now := time.Now().UTC()
	actorID := uuid.NewString()
	definition, err := MinimumLoopDefinition(uuid.NewString(), actorID, now)
	if err != nil {
		t.Fatal(err)
	}
	run := syntheticRun(definition, actorID)
	exact := syntheticSlice("compiler-retry", "pages")
	stale := exact
	stale.PageSpec = nil
	stale.Prototype = nil
	run.Context.Slices[stale.ID] = stale

	review := addSyntheticNode(run, instanceKey("prototype-review", stale.ID), "prototype-review", stale.ID, NodeCompleted)
	run.Context.Nodes[review.Key] = NodeMetadata{
		DefinitionNodeID: review.DefinitionNodeID,
		SliceID:          stale.ID,
		Output:           json.RawMessage(`{"payload":{"approved":true}}`),
	}
	merge := addSyntheticNode(run, "pages-merged", "pages-merged", "", NodeRunning)
	mergeInputs, err := buildNodeInputEnvelope(run, definition, merge)
	if err != nil {
		t.Fatal(err)
	}
	refs := mergeInputs.SliceRefs()
	if len(refs) != 1 || refs[0].PageSpec != nil || refs[0].Prototype != nil {
		t.Fatalf("test setup did not freeze the historical incomplete merge lineage: %+v", refs)
	}
	merge.Status = NodeCompleted
	run.Context.Nodes[merge.Key] = NodeMetadata{
		DefinitionNodeID: merge.DefinitionNodeID,
		Input:            mergeInputs.Canonical(),
	}

	// This is the persisted state of a run whose HumanEdit submissions were
	// accepted before the completed merge's old edge snapshot was compiled.
	run.Context.Slices[exact.ID] = exact
	compiler := addSyntheticNode(run, "compile-manifest", "compile-manifest", "", NodeRunning)
	compilerInputs, err := buildNodeInputEnvelope(run, definition, compiler)
	if err != nil {
		t.Fatal(err)
	}
	assertSlicePrototype(t, compilerInputs.SliceRefs(), exact.ID, *exact.Prototype)
	compilerRefs := compilerInputs.SliceRefs()
	if compilerRefs[0].PageSpec == nil || !compilerRefs[0].PageSpec.Equal(*exact.PageSpec) {
		t.Fatalf("compiler retry did not recover the exact PageSpec lineage: %+v", compilerRefs)
	}
}

func TestSliceLineageEnrichmentDoesNotOverwriteExactOrConflictingIdentity(t *testing.T) {
	storedSlice := syntheticSlice("stored", "pages")
	stored := workflowSliceRef(storedSlice)
	current := stored
	replacement := platformRef("replacement-prototype")
	current.Prototype = &replacement
	enriched := enrichSliceRef(stored, current)
	if enriched.Prototype == nil || !enriched.Prototype.Equal(*stored.Prototype) {
		t.Fatalf("enrichment overwrote an exact immutable Prototype pin: stored=%+v current=%+v enriched=%+v", stored, current, enriched)
	}

	missing := stored
	missing.Prototype = nil
	conflictingIdentity := current
	conflictingIdentity.Key = "different-slice-key"
	enriched = enrichSliceRef(missing, conflictingIdentity)
	if enriched.Prototype != nil {
		t.Fatalf("enrichment accepted a conflicting slice identity: %+v", enriched)
	}
}

func assertSlicePrototype(t *testing.T, refs []domain.WorkflowSliceRef, sliceID string, expected domain.ArtifactRef) {
	t.Helper()
	if len(refs) != 1 || refs[0].ID != sliceID || refs[0].Prototype == nil || !refs[0].Prototype.Equal(expected) {
		t.Fatalf("slice %s lost exact Prototype %s: %+v", sliceID, expected.RevisionID, refs)
	}
}

func TestHumanEditSameArtifactRevisionReplacesPriorGenerationBaseline(t *testing.T) {
	b0 := platformRef("brief-b0")
	b1 := platformRef("brief-b1")
	b1.ArtifactID = b0.ArtifactID
	r1 := platformRef("requirements-r1")
	run := &RunRecord{ID: uuid.NewString(), Context: NewRunContext()}

	briefInput, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "brief-ai-edit", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{
			RunID: run.ID, NodeKey: "brief-ai", DefinitionNodeID: "brief-ai",
			ArtifactRevisions: []domain.ArtifactRef{b0},
		},
		Output: json.RawMessage(`{}`), Value: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	briefOutput := mustJSON(map[string]any{"artifactRevision": b1})
	briefReference := nodeOutputReference(
		run,
		domain.NodeDefinition{ID: "brief-edit", Type: domain.NodeHumanEdit, HumanEdit: &domain.HumanEditNodeConfig{ArtifactKind: "project_brief"}},
		&NodeRecord{Key: "brief-edit", DefinitionNodeID: "brief-edit", Type: domain.NodeHumanEdit, Status: NodeCompleted},
		briefOutput, briefInput, true, "",
	)
	if len(briefReference.ArtifactRevisions) != 1 || !briefReference.ArtifactRevisions[0].Equal(b1) {
		t.Fatalf("B1 did not replace B0 on the same stable artifact: %+v", briefReference.ArtifactRevisions)
	}

	requirementsAIInput, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "brief-review-requirements-ai", FromPort: "default", ToPort: "default", Source: briefReference,
		Output: briefOutput, Value: briefOutput,
	}})
	if err != nil {
		t.Fatal(err)
	}
	requirementsAIReference := nodeOutputReference(
		run,
		domain.NodeDefinition{ID: "requirements-ai", Type: domain.NodeAITransform, AITransform: &domain.AITransformNodeConfig{JobType: "derive_requirements"}},
		&NodeRecord{Key: "requirements-ai", DefinitionNodeID: "requirements-ai", Type: domain.NodeAITransform, Status: NodeCompleted},
		json.RawMessage(`{}`), requirementsAIInput, true, "",
	)
	requirementsEditInput, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "requirements-ai-edit", FromPort: "default", ToPort: "default", Source: requirementsAIReference,
		Output: json.RawMessage(`{}`), Value: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	requirementsReference := nodeOutputReference(
		run,
		domain.NodeDefinition{ID: "requirements-edit", Type: domain.NodeHumanEdit, HumanEdit: &domain.HumanEditNodeConfig{ArtifactKind: "product_requirements"}},
		&NodeRecord{Key: "requirements-edit", DefinitionNodeID: "requirements-edit", Type: domain.NodeHumanEdit, Status: NodeCompleted},
		mustJSON(map[string]any{"artifactRevision": r1}), requirementsEditInput, true, "",
	)
	if len(requirementsReference.ArtifactRevisions) != 2 || !containsArtifactRef(requirementsReference.ArtifactRevisions, b1) || !containsArtifactRef(requirementsReference.ArtifactRevisions, r1) || containsArtifactRef(requirementsReference.ArtifactRevisions, b0) {
		t.Fatalf("requirements baseline must be exactly B1+R1, never B0+R1: %+v", requirementsReference.ArtifactRevisions)
	}
}

func containsArtifactRef(refs []domain.ArtifactRef, candidate domain.ArtifactRef) bool {
	for _, ref := range refs {
		if ref.Equal(candidate) {
			return true
		}
	}
	return false
}

func TestMinimumLoopRuntimeInputsConnectArtifactToPublish(t *testing.T) {
	now := time.Now().UTC()
	userID := uuid.NewString()
	definition, err := MinimumLoopDefinition(uuid.NewString(), userID, now)
	if err != nil {
		t.Fatal(err)
	}
	run := syntheticRun(definition, userID)
	source := addSyntheticNode(run, "source", "source", "", NodeCompleted)
	run.Context.Nodes[source.Key] = NodeMetadata{DefinitionNodeID: "source", Output: json.RawMessage(`{"payload":{"manifestId":"input"}}`)}
	previous := source
	for _, nodeID := range []string{"project-brief-ai", "project-brief-edit", "project-brief-review", "requirements-ai", "requirements-edit", "requirements-review", "blueprint-ai", "blueprint-edit", "blueprint-review"} {
		node := addSyntheticNode(run, nodeID, nodeID, "", NodeRunning)
		inputs, err := buildNodeInputEnvelope(run, definition, node)
		if err != nil {
			t.Fatalf("%s input: %v", nodeID, err)
		}
		metadata := run.Context.Nodes[node.Key]
		metadata.Input = inputs.Canonical()
		metadata.Output = mustJSON(map[string]any{"payload": map[string]any{"completedNode": nodeID}})
		run.Context.Nodes[node.Key] = metadata
		node.Status = NodeCompleted
		previous = node
	}
	_ = previous

	pages := addSyntheticNode(run, "pages", "pages", "", NodeRunning)
	pageInputs, err := buildNodeInputEnvelope(run, definition, pages)
	if err != nil {
		t.Fatal(err)
	}
	slice := syntheticSlice("home", "pages")
	run.Context.Slices[slice.ID] = slice
	pages.Status = NodeCompleted
	run.Context.Nodes[pages.Key] = NodeMetadata{DefinitionNodeID: "pages", Input: pageInputs.Canonical(), FanOutOutputs: map[string]json.RawMessage{slice.ID: mustJSON(FanOutItem{Key: slice.Key, Title: slice.Title, Blueprint: slice.Blueprint, Prototype: slice.Prototype})}}

	for _, nodeID := range []string{"page-spec-ai", "page-spec-edit", "page-spec-review", "prototype-ai", "prototype-edit", "prototype-review"} {
		key := instanceKey(nodeID, slice.ID)
		node := addSyntheticNode(run, key, nodeID, slice.ID, NodeRunning)
		inputs, err := buildNodeInputEnvelope(run, definition, node)
		if err != nil {
			t.Fatalf("%s input: %v", nodeID, err)
		}
		metadata := run.Context.Nodes[key]
		metadata.Input = inputs.Canonical()
		metadata.Output = mustJSON(map[string]any{"payload": map[string]any{"completedNode": nodeID}})
		run.Context.Nodes[key] = metadata
		node.Status = NodeCompleted
	}
	merge := addSyntheticNode(run, "pages-merged", "pages-merged", "", NodeRunning)
	mergeInputs, err := buildNodeInputEnvelope(run, definition, merge)
	if err != nil {
		t.Fatal(err)
	}
	merge.Status = NodeCompleted
	run.Context.Nodes[merge.Key] = NodeMetadata{DefinitionNodeID: merge.DefinitionNodeID, Input: mergeInputs.Canonical()}

	compiler := addSyntheticNode(run, "compile-manifest", "compile-manifest", "", NodeRunning)
	compilerInputs, err := buildNodeInputEnvelope(run, definition, compiler)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlySlice(t, compilerInputs.SliceRefs(), slice)
	buildManifest := BuildManifest{SchemaVersion: 1, ProjectID: run.ProjectID, RunID: run.ID, ManifestGroupKey: uuid.NewString(), SliceIDs: []string{slice.ID}, BundleIDs: []string{uuid.NewString()}, Sources: []domain.ArtifactRef{*slice.Prototype}, Constraints: json.RawMessage(`{}`), CreatedAt: now}
	if err := buildManifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	compiler.Status = NodeCompleted
	run.Context.Nodes[compiler.Key] = NodeMetadata{DefinitionNodeID: compiler.DefinitionNodeID, Input: compilerInputs.Canonical(), Output: mustJSON(buildManifest)}

	workbench := addSyntheticNode(run, "workbench", "workbench", "", NodeRunning)
	workbenchInputs, err := buildNodeInputEnvelope(run, definition, workbench)
	if err != nil {
		t.Fatal(err)
	}
	workbenchDefinition, _ := definition.FindNode("workbench")
	if manifest, err := buildManifestFromExecution(Execution{Run: *run, Node: *workbench, Definition: workbenchDefinition, Inputs: workbenchInputs}); err != nil || manifest.Hash != buildManifest.Hash {
		t.Fatalf("workbench did not receive compiler output: manifest=%+v err=%v", manifest, err)
	}
	workbench.Status = NodeCompleted
	run.Context.Nodes[workbench.Key] = NodeMetadata{DefinitionNodeID: workbench.DefinitionNodeID, Input: workbenchInputs.Canonical(), Output: json.RawMessage(`{"payload":{"workspaceRevision":"workspace-1"}}`)}

	quality := addSyntheticNode(run, "quality", "quality", "", NodeRunning)
	qualityInputs, err := buildNodeInputEnvelope(run, definition, quality)
	if err != nil {
		t.Fatal(err)
	}
	qualityValue, qualitySource, ok := qualityInputs.FirstValue("default")
	if !ok || qualitySource.DefinitionNodeID != "workbench" || !strings.Contains(string(qualityValue), "workspaceRevision") {
		t.Fatalf("quality did not receive exact Workbench output: source=%+v value=%s", qualitySource, qualityValue)
	}
	quality.Status = NodeCompleted
	run.Context.Nodes[quality.Key] = NodeMetadata{DefinitionNodeID: quality.DefinitionNodeID, Input: qualityInputs.Canonical(), Output: json.RawMessage(`{"payload":{"qualityRunId":"quality-1","passed":true}}`)}

	publish := addSyntheticNode(run, "publish", "publish", "", NodeRunning)
	publishInputs, err := buildNodeInputEnvelope(run, definition, publish)
	if err != nil {
		t.Fatal(err)
	}
	publishValue, publishSource, ok := publishInputs.FirstValue("default")
	if !ok || publishSource.DefinitionNodeID != "quality" || !strings.Contains(string(publishValue), "qualityRunId") {
		t.Fatalf("publish did not receive exact Quality output: source=%+v value=%s", publishSource, publishValue)
	}
}

func syntheticRun(definition domain.WorkflowDefinition, actorID string) *RunRecord {
	return &RunRecord{
		ID: uuid.NewString(), ProjectID: uuid.NewString(), DefinitionVersionID: uuid.NewString(), Definition: definition.Ref(), Status: RunRunning,
		Scope: json.RawMessage(`{}`), Context: NewRunContext(), StartedBy: actorID, Nodes: map[string]*NodeRecord{},
	}
}

func addSyntheticNode(run *RunRecord, key, definitionNodeID, sliceID string, status NodeStatus) *NodeRecord {
	node := &NodeRecord{ID: uuid.NewString(), RunID: run.ID, Key: key, DefinitionNodeID: definitionNodeID, SliceID: sliceID, Status: status}
	run.Nodes[key] = node
	metadata := run.Context.Nodes[key]
	metadata.DefinitionNodeID = definitionNodeID
	metadata.SliceID = sliceID
	run.Context.Nodes[key] = metadata
	return node
}

func syntheticSlice(key, fanOutNodeID string) SliceContext {
	prototype := platformRef("prototype-" + key)
	return SliceContext{ID: uuid.NewString(), Key: key, Title: strings.ToUpper(key), FanOutNodeID: fanOutNodeID, Blueprint: platformRef("blueprint-" + key), PageSpec: ptrArtifactRef(platformRef("page-" + key)), Prototype: &prototype}
}

func ptrArtifactRef(ref domain.ArtifactRef) *domain.ArtifactRef { return &ref }

func prepareFanOutRegion(t *testing.T, run *RunRecord, definition domain.WorkflowDefinition, fanOutID, workID, mergeID string, slice SliceContext) {
	t.Helper()
	item := FanOutItem{Key: slice.Key, Title: slice.Title, Blueprint: slice.Blueprint, PageSpec: slice.PageSpec, Prototype: slice.Prototype}
	fanOut := addSyntheticNode(run, fanOutID, fanOutID, "", NodeCompleted)
	run.Context.Nodes[fanOut.Key] = NodeMetadata{DefinitionNodeID: fanOutID, FanOutOutputs: map[string]json.RawMessage{slice.ID: mustJSON(item)}}
	work := addSyntheticNode(run, instanceKey(workID, slice.ID), workID, slice.ID, NodeRunning)
	workInputs, err := buildNodeInputEnvelope(run, definition, work)
	if err != nil {
		t.Fatal(err)
	}
	work.Status = NodeCompleted
	run.Context.Nodes[work.Key] = NodeMetadata{DefinitionNodeID: workID, SliceID: slice.ID, Input: workInputs.Canonical(), Output: mustJSON(map[string]any{"slice": slice.Key})}
	merge := addSyntheticNode(run, mergeID, mergeID, "", NodeRunning)
	mergeInputs, err := buildNodeInputEnvelope(run, definition, merge)
	if err != nil {
		t.Fatal(err)
	}
	merge.Status = NodeCompleted
	run.Context.Nodes[merge.Key] = NodeMetadata{DefinitionNodeID: mergeID, Input: mergeInputs.Canonical()}
}

func assertOnlySlice(t *testing.T, refs []domain.WorkflowSliceRef, expected SliceContext) {
	t.Helper()
	if len(refs) != 1 || refs[0].ID != expected.ID || refs[0].FanOutNodeID != expected.FanOutNodeID {
		t.Fatalf("expected only slice %s/%s, got %+v", expected.FanOutNodeID, expected.ID, refs)
	}
}
