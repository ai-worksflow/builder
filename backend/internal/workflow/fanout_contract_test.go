package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

func fanOutResolverExecution(
	t *testing.T,
	config domain.FanOutNodeConfig,
	value json.RawMessage,
) Execution {
	t.Helper()
	envelope, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "incoming", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{
			RunID: uuid.NewString(), NodeKey: "source", DefinitionNodeID: "source",
		},
		Output: value, Value: value,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return Execution{
		Run: RunRecord{ID: uuid.NewString(), ProjectID: uuid.NewString(), Context: NewRunContext()},
		Definition: domain.NodeDefinition{
			ID: "fan", Name: "Fan", Type: domain.NodeFanOut,
			FanOut: &config,
		},
		Inputs: envelope,
	}
}

func TestInputEnvelopeFanOutUsesConfiguredPointersAndPreservesPayload(t *testing.T) {
	execution := fanOutResolverExecution(t, domain.FanOutNodeConfig{
		ItemsPath: "/payload/jobs", SliceKeyPath: "/identity/key",
		MergeNodeID: "merge", MaxParallel: 2, ItemKind: "generic",
	}, json.RawMessage(`{"payload":{"jobs":[{"identity":{"key":"alpha"},"title":"Alpha job","value":1},{"identity":{"key":"beta"},"value":2}]}}`))
	items, err := (InputEnvelopeFanOutResolver{}).Resolve(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Key != "alpha" || items[0].Title != "Alpha job" || items[1].Key != "beta" || items[1].Title != "beta" {
		t.Fatalf("configured pointers did not resolve stable generic items: %+v", items)
	}
	if string(items[0].Payload) != `{"identity":{"key":"alpha"},"title":"Alpha job","value":1}` {
		t.Fatalf("fan-out rewrote the immutable item payload: %s", items[0].Payload)
	}
	if items[0].Blueprint.ArtifactID != "" || items[0].PageSpec != nil {
		t.Fatalf("generic fan-out invented delivery artifact lineage: %+v", items[0])
	}
}

func TestDeliverySliceFanOutModeRequiresExactBlueprintAndPageSpecRefs(t *testing.T) {
	blueprint := platformRef("delivery-blueprint")
	pageSpec := platformRef("delivery-page-spec")
	value, err := domain.CanonicalJSON(map[string]any{
		"workflowContext": map[string]any{
			"deliverySlices": []any{map[string]any{
				"key": "page.orders", "title": "Orders",
				"blueprint": blueprint, "pageSpec": pageSpec,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution := fanOutResolverExecution(t, domain.FanOutNodeConfig{
		ItemsPath: "/workflowContext/deliverySlices", SliceKeyPath: "/key",
		MergeNodeID: "merge", MaxParallel: 2, ItemKind: "delivery_slice",
	}, value)
	items, err := (DefinitionFanOutResolver{}).Resolve(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Blueprint.Equal(blueprint) || items[0].PageSpec == nil || !items[0].PageSpec.Equal(pageSpec) {
		t.Fatalf("delivery slice exact refs were not preserved: %+v", items)
	}
	contextSlices, err := domain.CanonicalJSON([]any{map[string]any{
		"key": "page.orders", "title": "Orders",
		"blueprint": blueprint, "pageSpec": pageSpec,
	}})
	if err != nil {
		t.Fatal(err)
	}
	execution.Run.Context.Values["deliverySlices"] = contextSlices
	hooked, err := (DefinitionFanOutResolver{
		DeliverySlices: ContextFanOutResolver{ValueKey: "deliverySlices"},
	}).Resolve(context.Background(), execution)
	if err != nil || len(hooked) != 1 || !hooked[0].Blueprint.Equal(blueprint) || hooked[0].PageSpec == nil || !hooked[0].PageSpec.Equal(pageSpec) {
		t.Fatalf("production delivery-slice hook did not retain exact refs: items=%+v err=%v", hooked, err)
	}

	missingPageSpec := fanOutResolverExecution(t, *execution.Definition.FanOut, json.RawMessage(`{"workflowContext":{"deliverySlices":[{"key":"page.orders","title":"Orders","blueprint":{"artifactId":"a","revisionId":"r","contentHash":"sha256:invalid"}}]}}`))
	if _, err := (DefinitionFanOutResolver{}).Resolve(context.Background(), missingPageSpec); err == nil {
		t.Fatal("delivery slice mode accepted missing or malformed exact refs")
	}
}

func blueprintPageResolverExecution(
	t *testing.T,
	projectID, actorID string,
	ref domain.ArtifactRef,
) Execution {
	t.Helper()
	runID := uuid.NewString()
	envelope, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "blueprint-review-pages", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{
			RunID: runID, NodeKey: "blueprint-review", DefinitionNodeID: "blueprint-review",
			ArtifactRevisions: []domain.ArtifactRef{ref},
		},
		Output: json.RawMessage(`{}`), Value: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return Execution{
		Run: RunRecord{ID: runID, ProjectID: projectID, StartedBy: actorID, Context: NewRunContext()},
		Definition: domain.NodeDefinition{
			ID: "pages", Name: "Blueprint pages", Type: domain.NodeFanOut,
			FanOut: &domain.FanOutNodeConfig{ItemsPath: "/blueprintPages", SliceKeyPath: "/key", MergeNodeID: "merge", MaxParallel: 2, ItemKind: "blueprint_page"},
		},
		Inputs: envelope,
	}
}

func blueprintPageResolverFixture(t *testing.T) (Execution, *fakeHumanEditArtifacts, domain.ArtifactRef) {
	t.Helper()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	ref := platformRef("approved-blueprint-pages")
	latest, approved := ref.RevisionID, ref.RevisionID
	content := json.RawMessage(`{
		"nodes":[{"id":"legacy-layout","key":"LEGACY","kind":"page","title":"Ignored legacy layout","route":"/ignored","userGoal":"Ignored"}],
		"semantic":{"nodes":[
			{"id":"page-orders","key":"PAGE-ORDERS","kind":"page","title":"Orders","route":"/orders","userGoal":"Review open orders","requirementIds":["REQ-1"]},
			{"id":"feature-orders","key":"FEATURE-ORDERS","kind":"feature","title":"Orders feature"},
			{"id":"page-home","key":"PAGE-HOME","kind":"page","title":"Home","route":"/","userGoal":"Understand current status","requirementIds":["REQ-2"]}
		]}
	}`)
	artifacts := &fakeHumanEditArtifacts{
		artifacts: map[string]core.VersionedArtifact{
			ref.ArtifactID: {Artifact: core.Artifact{
				ID: ref.ArtifactID, ProjectID: projectID, Kind: "blueprint", SyncStatus: "current",
				LatestRevisionID: &latest, LatestApprovedRevisionID: &approved,
			}},
		},
		revisions: map[string]core.ArtifactRevision{
			ref.RevisionID: {
				ID: ref.RevisionID, ArtifactID: ref.ArtifactID, ContentHash: ref.ContentHash,
				Content: content, WorkflowStatus: "approved",
			},
		},
	}
	return blueprintPageResolverExecution(t, projectID, actorID, ref), artifacts, ref
}

func TestCoreBlueprintPageFanOutResolverUsesCurrentApprovedSemanticPages(t *testing.T) {
	execution, artifacts, blueprint := blueprintPageResolverFixture(t)
	items, err := (CoreBlueprintPageFanOutResolver{Artifacts: artifacts}).Resolve(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Key != "PAGE-HOME" || items[1].Key != "PAGE-ORDERS" {
		t.Fatalf("semantic Blueprint Pages were not deterministically resolved: %+v", items)
	}
	for _, item := range items {
		if item.Blueprint.ArtifactID != blueprint.ArtifactID || item.Blueprint.RevisionID != blueprint.RevisionID ||
			item.Blueprint.ContentHash != blueprint.ContentHash || item.Blueprint.AnchorID == "" || item.PageSpec != nil {
			t.Fatalf("Blueprint Page fan-out lost exact anchored lineage: %+v", item)
		}
		var payload struct {
			PageNodeID string             `json:"pageNodeId"`
			Route      string             `json:"route"`
			UserGoal   string             `json:"userGoal"`
			Blueprint  domain.ArtifactRef `json:"blueprint"`
		}
		if json.Unmarshal(item.Payload, &payload) != nil || payload.PageNodeID != item.Blueprint.AnchorID || payload.Route == "" || payload.UserGoal == "" || !payload.Blueprint.Equal(item.Blueprint) {
			t.Fatalf("Blueprint Page route/goal snapshot is not frozen: %s", item.Payload)
		}
	}
}

func TestCoreBlueprintPageFanOutResolverConsumesOnlyFrozenSelection(t *testing.T) {
	execution, artifacts, blueprint := blueprintPageResolverFixture(t)
	pageOrders, prototypeOrders := platformRef("selection-orders-page-spec"), platformRef("selection-orders-prototype")
	ordersManifest := workflowSelectionManifest(t, execution.Run.ProjectID, execution.Run.StartedBy, blueprint, "page-orders", pageOrders, prototypeOrders)
	execution.Run.InputManifest = ptrManifest(ordersManifest.Ref())
	orders, err := (CoreBlueprintPageFanOutResolver{
		Artifacts: artifacts, Proposals: &fakeCoreProposals{manifest: ordersManifest},
	}).Resolve(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].Blueprint.AnchorID != "page-orders" || orders[0].PageSpec == nil ||
		!orders[0].PageSpec.Equal(pageOrders) || orders[0].Prototype == nil || !orders[0].Prototype.Equal(prototypeOrders) {
		t.Fatalf("orders selection fan-out = %+v", orders)
	}

	pageHome, prototypeHome := platformRef("selection-home-page-spec"), platformRef("selection-home-prototype")
	homeManifest := workflowSelectionManifest(t, execution.Run.ProjectID, execution.Run.StartedBy, blueprint, "page-home", pageHome, prototypeHome)
	execution.Run.InputManifest = ptrManifest(homeManifest.Ref())
	home, err := (CoreBlueprintPageFanOutResolver{
		Artifacts: artifacts, Proposals: &fakeCoreProposals{manifest: homeManifest},
	}).Resolve(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(home) != 1 || home[0].Blueprint.AnchorID != "page-home" || home[0].PageSpec == nil ||
		!home[0].PageSpec.Equal(pageHome) || home[0].Prototype == nil || !home[0].Prototype.Equal(prototypeHome) {
		t.Fatalf("home selection fan-out = %+v", home)
	}
	if home[0].PageSpec.Equal(pageOrders) || home[0].Prototype.Equal(prototypeOrders) {
		t.Fatal("independent Blueprint selection fan-outs shared frozen inputs")
	}
}

func TestBlueprintSelectionPageFanOutRejectsOrdinaryBlueprintInput(t *testing.T) {
	execution, artifacts, _ := blueprintPageResolverFixture(t)
	execution.Definition.FanOut.ItemKind = "blueprint_selection_page"
	if _, err := (CoreBlueprintPageFanOutResolver{Artifacts: artifacts}).Resolve(context.Background(), execution); err == nil ||
		!strings.Contains(err.Error(), "requires a frozen Blueprint selection manifest") {
		t.Fatalf("selection-only fan-out accepted an ordinary Blueprint input: %v", err)
	}
}

func workflowSelectionManifest(
	t *testing.T,
	projectID, actorID string,
	blueprint domain.ArtifactRef,
	nodeID string,
	pageSpec, prototype domain.ArtifactRef,
) domain.InputManifest {
	t.Helper()
	selectionID := platformHash(blueprint.RevisionID + "\x00" + nodeID)
	anchored := blueprint
	anchored.AnchorID = nodeID
	scope := workflowBlueprintSelectionScope{
		SchemaVersion: 1, SelectionID: selectionID, Blueprint: blueprint, NodeIDs: []string{nodeID},
		PageBindings: []workflowBlueprintSelectionPageBinding{{NodeID: nodeID, PageSpec: &pageSpec, Prototype: &prototype}},
	}
	constraints, err := domain.CanonicalJSON(map[string]any{"blueprintSelection": scope})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, core.BlueprintSelectionJobType, selectionID, nil,
		[]domain.ManifestSource{
			{Ref: blueprint, Purpose: "blueprint_selection_root"},
			{Ref: anchored, Purpose: "blueprint_selection_node"},
			{Ref: pageSpec, Purpose: "selected_page_spec"},
			{Ref: prototype, Purpose: "selected_prototype"},
		},
		constraints, "blueprint-selection/v1", actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestCoreBlueprintPageFanOutResolverFailsClosedOnStaleOrForgedInput(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*Execution, *fakeHumanEditArtifacts, *domain.ArtifactRef)
	}{
		{
			name: "wrong hash",
			mutate: func(execution *Execution, _ *fakeHumanEditArtifacts, ref *domain.ArtifactRef) {
				ref.ContentHash = platformHash("forged")
				*execution = blueprintPageResolverExecution(t, execution.Run.ProjectID, execution.Run.StartedBy, *ref)
			},
		},
		{
			name: "needs sync",
			mutate: func(_ *Execution, artifacts *fakeHumanEditArtifacts, ref *domain.ArtifactRef) {
				value := artifacts.artifacts[ref.ArtifactID]
				value.Artifact.SyncStatus = "needs_sync"
				artifacts.artifacts[ref.ArtifactID] = value
			},
		},
		{
			name: "unapproved revision",
			mutate: func(_ *Execution, artifacts *fakeHumanEditArtifacts, ref *domain.ArtifactRef) {
				value := artifacts.revisions[ref.RevisionID]
				value.WorkflowStatus = "draft"
				artifacts.revisions[ref.RevisionID] = value
			},
		},
		{
			name: "cross project",
			mutate: func(_ *Execution, artifacts *fakeHumanEditArtifacts, ref *domain.ArtifactRef) {
				value := artifacts.artifacts[ref.ArtifactID]
				value.Artifact.ProjectID = uuid.NewString()
				artifacts.artifacts[ref.ArtifactID] = value
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			execution, artifacts, ref := blueprintPageResolverFixture(t)
			testCase.mutate(&execution, artifacts, &ref)
			if _, err := (CoreBlueprintPageFanOutResolver{Artifacts: artifacts}).Resolve(context.Background(), execution); err == nil {
				t.Fatal("stale or forged Blueprint fan-out input was accepted")
			}
		})
	}
}

func TestBlueprintPageResolverRunsThroughEngineWithAnchoredSlices(t *testing.T) {
	openSchema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	userID := uuid.NewString()
	nodes := []domain.NodeDefinition{
		{ID: "source", Name: "Approved Blueprint", Type: domain.NodeTransform, InputSchema: openSchema, OutputSchema: openSchema, Transform: &domain.TransformNodeConfig{Transform: "source"}},
		{ID: "pages", Name: "Blueprint pages", Type: domain.NodeFanOut, InputSchema: openSchema, OutputSchema: openSchema, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/blueprintPages", SliceKeyPath: "/key", MergeNodeID: "merge", MaxParallel: 2, ItemKind: "blueprint_page"}},
		{ID: "page-work", Name: "Page work", Type: domain.NodeTransform, InputSchema: openSchema, OutputSchema: openSchema, Transform: &domain.TransformNodeConfig{Transform: "page"}},
		{ID: "merge", Name: "Merge", Type: domain.NodeMerge, InputSchema: openSchema, OutputSchema: openSchema, Merge: &domain.MergeNodeConfig{FanOutNodeID: "pages", Policy: domain.MergeAll}},
	}
	definition, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Blueprint Page fan-out", "2", nodes,
		[]domain.WorkflowEdge{{ID: "source-pages", From: "source", To: "pages"}, {ID: "pages-work", From: "pages", To: "page-work"}, {ID: "work-merge", From: "page-work", To: "merge"}},
		userID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewMapRegistry()
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	blueprint := platformRef("engine-blueprint-pages")
	latest, approved := blueprint.RevisionID, blueprint.RevisionID
	artifactAPI := &fakeHumanEditArtifacts{
		artifacts: map[string]core.VersionedArtifact{blueprint.ArtifactID: {Artifact: core.Artifact{
			ID: blueprint.ArtifactID, ProjectID: projectID, Kind: "blueprint", SyncStatus: "current",
			LatestRevisionID: &latest, LatestApprovedRevisionID: &approved,
		}}},
		revisions: map[string]core.ArtifactRevision{blueprint.RevisionID: {
			ID: blueprint.RevisionID, ArtifactID: blueprint.ArtifactID, ContentHash: blueprint.ContentHash, WorkflowStatus: "approved",
			Content: json.RawMessage(`{"semantic":{"nodes":[{"id":"page-a","key":"PAGE-A","kind":"page","title":"A","route":"/a","userGoal":"Use A"},{"id":"page-b","key":"PAGE-B","kind":"page","title":"B","route":"/b","userGoal":"Use B"}]}}`),
		}},
	}
	processed := map[string]bool{}
	if err := registry.Register(domain.NodeTransform, RunnerFunc(func(_ context.Context, execution Execution) (WorkerResult, error) {
		if execution.Definition.ID == "source" {
			return WorkerResult{Disposition: ResultComplete, Output: humanEditOutput(t, blueprint)}, nil
		}
		value, source, ok := execution.Inputs.FirstValue("default")
		if !ok || len(source.DeliverySliceRefs) != 1 || source.DeliverySliceRefs[0].Blueprint == nil || source.DeliverySliceRefs[0].Blueprint.AnchorID == "" {
			return WorkerResult{}, context.Canceled
		}
		var page struct {
			Key   string `json:"key"`
			Route string `json:"route"`
		}
		if json.Unmarshal(value, &page) != nil || page.Key == "" || page.Route == "" {
			return WorkerResult{}, context.Canceled
		}
		processed[page.Key] = true
		return WorkerResult{Disposition: ResultComplete, Output: value}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(domain.NodeFanOut, FanOutRunner{Resolver: DefinitionFanOutResolver{BlueprintPages: CoreBlueprintPageFanOutResolver{Artifacts: artifactAPI}}}); err != nil {
		t.Fatal(err)
	}
	engine.Runners = registry
	run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 5; iteration++ {
		if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
			t.Fatalf("Blueprint Page engine iteration %d: %v", iteration, err)
		}
	}
	completed, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != RunCompleted || len(completed.Context.Slices) != 2 || !processed["PAGE-A"] || !processed["PAGE-B"] {
		t.Fatalf("Blueprint Page engine flow did not complete: status=%s slices=%d processed=%v", completed.Status, len(completed.Context.Slices), processed)
	}
	for _, slice := range completed.Context.Slices {
		if slice.Blueprint.AnchorID == "" || slice.PageSpec != nil {
			t.Fatalf("engine did not instantiate an anchored pre-PageSpec slice: %+v", slice)
		}
	}
}

func TestGenericFanOutAndMergeRunWithoutDeliveryBlueprints(t *testing.T) {
	openSchema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	sourceSchema := json.RawMessage(`{"type":"object","properties":{"payload":{"type":"object"}},"required":["payload"]}`)
	userID := uuid.NewString()
	nodes := []domain.NodeDefinition{
		{ID: "source", Name: "Source", Type: domain.NodeTransform, InputSchema: openSchema, OutputSchema: sourceSchema, Transform: &domain.TransformNodeConfig{Transform: "source"}},
		{ID: "fan", Name: "Fan", Type: domain.NodeFanOut, InputSchema: sourceSchema, OutputSchema: openSchema, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/payload/jobs", SliceKeyPath: "/id", MergeNodeID: "merge", MaxParallel: 2, ItemKind: "generic"}},
		{ID: "work", Name: "Work", Type: domain.NodeTransform, InputSchema: openSchema, OutputSchema: openSchema, Transform: &domain.TransformNodeConfig{Transform: "work"}},
		{ID: "merge", Name: "Merge", Type: domain.NodeMerge, InputSchema: openSchema, OutputSchema: openSchema, Merge: &domain.MergeNodeConfig{FanOutNodeID: "fan", Policy: domain.MergeAll}},
	}
	definition, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Generic fan-out", "2", nodes,
		[]domain.WorkflowEdge{
			{ID: "source-fan", From: "source", To: "fan"},
			{ID: "fan-work", From: "fan", To: "work"},
			{ID: "work-merge", From: "work", To: "merge"},
		},
		userID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	processed := map[string]bool{}
	registry := NewMapRegistry()
	if err := registry.Register(domain.NodeTransform, RunnerFunc(func(_ context.Context, execution Execution) (WorkerResult, error) {
		if execution.Definition.ID == "source" {
			return WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"payload":{"jobs":[{"id":"alpha","value":1},{"id":"beta","value":2}]}}`)}, nil
		}
		value, _, ok := execution.Inputs.FirstValue("default")
		if !ok {
			return WorkerResult{}, context.Canceled
		}
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(value, &item); err != nil || strings.TrimSpace(item.ID) == "" {
			return WorkerResult{}, context.Canceled
		}
		processed[item.ID] = true
		return WorkerResult{Disposition: ResultComplete, Output: value}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(domain.NodeFanOut, FanOutRunner{Resolver: DefinitionFanOutResolver{}}); err != nil {
		t.Fatal(err)
	}
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	run, err := engine.Start(context.Background(), StartRequest{
		RunID: uuid.NewString(), ProjectID: projectID,
		DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 5; iteration++ {
		if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
			t.Fatalf("generic fan-out iteration %d: %v", iteration, err)
		}
	}
	completed, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != RunCompleted || completed.Nodes["merge"].Status != NodeCompleted || len(completed.Context.Slices) != 2 || !processed["alpha"] || !processed["beta"] {
		t.Fatalf("generic fan-out/merge did not complete: run=%s merge=%s slices=%d processed=%v", completed.Status, completed.Nodes["merge"].Status, len(completed.Context.Slices), processed)
	}
	for _, slice := range completed.Context.Slices {
		if slice.Blueprint.ArtifactID != "" || len(slice.Payload) == 0 {
			t.Fatalf("generic slice was forced into delivery lineage or lost its payload: %+v", slice)
		}
	}
}
