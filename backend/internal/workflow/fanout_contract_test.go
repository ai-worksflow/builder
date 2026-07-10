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
