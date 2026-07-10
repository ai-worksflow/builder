package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type mutableClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *mutableClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.value }
func (c *mutableClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.value = c.value.Add(duration)
	c.mu.Unlock()
}

func engineSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"payload":{"type":"object"}},"required":["payload"]}`)
}

func engineReviewDecision(actorID string, resolution ReviewResolution, reason string) ReviewDecision {
	action := core.ActionReview
	if resolution == ReviewApprove || resolution == ReviewWaive {
		action = core.ActionApprove
	}
	return ReviewDecision{
		Resolution: resolution, Reason: reason,
		Actor: ActorProvenance{
			ActorID: actorID, Role: core.RoleOwner, Action: action,
			Source: ActorSourceAuthenticatedCommand, AuthorizedAt: time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC),
		},
	}
}

func authorizeTestExecution(t *testing.T, engine *Engine, runID, nodeKey string, role core.Role, action core.Action) {
	t.Helper()
	if err := engine.AuthorizeNodeExecution(context.Background(), runID, nodeKey, ActorProvenance{
		ActorID: uuid.NewString(), Role: role, Action: action,
		Source: ActorSourceAuthenticatedCommand, AuthorizedAt: engine.now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func testManifestForEngine(t *testing.T, projectID, userID string, now time.Time) domain.InputManifest {
	t.Helper()
	hash, _ := domain.CanonicalHash(map[string]any{"source": 1})
	source := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: hash}
	manifest, err := domain.NewInputManifest(uuid.NewString(), projectID, "workflow_start", "", nil, []domain.ManifestSource{{Ref: source, Purpose: "approved input"}}, json.RawMessage(`{"strict":true}`), "workflow/v1", userID, now)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func saveEngineDefinition(t *testing.T, store Store, projectID, userID string, definition domain.WorkflowDefinition) DefinitionRecord {
	t.Helper()
	record := DefinitionRecord{VersionID: uuid.NewString(), ProjectID: projectID, Key: "test", Title: definition.Name, Published: true, Definition: definition}
	if err := store.SaveDefinition(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return record
}

func newTestEngine(t *testing.T, definition domain.WorkflowDefinition, registry RunnerRegistry) (*Engine, *MemoryStore, *mutableClock, DefinitionRecord, domain.InputManifest, string, string) {
	t.Helper()
	projectID, userID := uuid.NewString(), uuid.NewString()
	clock := &mutableClock{value: time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)}
	store := NewMemoryStore(nil)
	record := saveEngineDefinition(t, store, projectID, userID, definition)
	manifest := testManifestForEngine(t, projectID, userID, clock.Now())
	if err := store.SaveManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	engine, _ := NewEngine(store)
	engine.Clock = clock
	engine.Runners = registry
	engine.RetryBackoff = func(int) time.Duration { return 0 }
	return engine, store, clock, record, manifest, projectID, userID
}

func simpleDefinition(t *testing.T, id, userID string, now time.Time) domain.WorkflowDefinition {
	t.Helper()
	schema := engineSchema()
	nodes := []domain.NodeDefinition{{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}}, {ID: "transform", Name: "Transform", Type: domain.NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &domain.TransformNodeConfig{Transform: "test"}}, {ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}}}
	definition, err := domain.NewWorkflowDefinition(id, 1, "Simple", "2", nodes, []domain.WorkflowEdge{{ID: "e1", From: "input", To: "transform"}, {ID: "e2", From: "transform", To: "publish"}}, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestConcurrentClaimHasSingleWinnerAndMonotonicEventSequence(t *testing.T) {
	userID := uuid.NewString()
	now := time.Now().UTC()
	definition := simpleDefinition(t, uuid.NewString(), userID, now)
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, nil)
	run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	if err != nil {
		t.Fatal(err)
	}
	claimNow := engine.Clock.Now()
	var winners atomic.Int32
	var group sync.WaitGroup
	for index := 0; index < 24; index++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			_, err := store.ClaimRunnable(context.Background(), fmt.Sprintf("worker-%d", worker), claimNow, time.Minute)
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrNoRunnableNode) {
				t.Errorf("unexpected claim error: %v", err)
			}
		}(index)
	}
	group.Wait()
	if winners.Load() != 1 {
		t.Fatalf("expected one winner, got %d", winners.Load())
	}
	events, _ := store.ListEvents(context.Background(), run.ID, 0, 100)
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event sequence gap: %+v", events)
		}
	}
}

func TestExpiredLeaseIsRecoveredExactlyOnce(t *testing.T) {
	userID := uuid.NewString()
	definition := simpleDefinition(t, uuid.NewString(), userID, time.Now())
	engine, store, clock, record, manifest, projectID, startedBy := newTestEngine(t, definition, nil)
	run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimRunnable(context.Background(), "worker-1", clock.Now(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	var winners atomic.Int32
	var recovered Lease
	var mu sync.Mutex
	var group sync.WaitGroup
	for _, worker := range []string{"worker-2", "worker-3"} {
		group.Add(1)
		go func(workerID string) {
			defer group.Done()
			lease, err := store.ClaimRunnable(context.Background(), workerID, clock.Now(), time.Minute)
			if err == nil {
				winners.Add(1)
				mu.Lock()
				recovered = lease
				mu.Unlock()
			} else if !errors.Is(err, ErrNoRunnableNode) {
				t.Errorf("recover: %v", err)
			}
		}(worker)
	}
	group.Wait()
	if winners.Load() != 1 || recovered.NodeID != first.NodeID || recovered.Attempt != 2 {
		t.Fatalf("unexpected lease recovery: winners=%d lease=%+v", winners.Load(), recovered)
	}
	if _, err := store.RenewLease(context.Background(), first, clock.Now(), time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old owner renewed recovered lease: %v", err)
	}
	events, _ := store.ListEvents(context.Background(), run.ID, 0, 100)
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatal("event sequence is not monotonic")
		}
	}
}

func TestEngineRetriesFailureAndResumesWithPinnedDefinition(t *testing.T) {
	userID := uuid.NewString()
	now := time.Now().UTC()
	definition := simpleDefinition(t, uuid.NewString(), userID, now)
	registry := NewMapRegistry()
	var attempts atomic.Int32
	_ = registry.Register(domain.NodeTransform, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		if attempts.Add(1) == 1 {
			return WorkerResult{}, fmt.Errorf("temporary")
		}
		return WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"payload":{}}`)}, nil
	}))
	_ = registry.Register(domain.NodePublish, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete}, nil
	}))
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	if err != nil {
		t.Fatal(err)
	}
	versionTwo, err := domain.NewWorkflowDefinition(definition.ID, 2, definition.Name, definition.SchemaVersion, definition.Nodes, definition.Edges, definition.CreatedBy, definition.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDefinition(context.Background(), DefinitionRecord{VersionID: uuid.NewString(), ProjectID: projectID, Key: "test", Title: versionTwo.Name, Published: true, Definition: versionTwo}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err == nil || err.Error() != "temporary" {
		t.Fatalf("expected runner failure, got %v", err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	authorizeTestExecution(t, engine, run.ID, "publish", core.RoleOwner, core.ActionPublish)
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	resumed, _ := store.GetRun(context.Background(), run.ID)
	if resumed.Status != RunCompleted || attempts.Load() != 2 {
		t.Fatalf("unexpected recovered run: %s attempts=%d", resumed.Status, attempts.Load())
	}
	if resumed.Definition != definition.Ref() {
		t.Fatal("run did not remain pinned to definition version")
	}
}

func TestRunnerOutputMustSatisfyDeclaredSchema(t *testing.T) {
	userID := uuid.NewString()
	definition := simpleDefinition(t, uuid.NewString(), userID, time.Now())
	registry := NewMapRegistry()
	_ = registry.Register(domain.NodeTransform, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"unexpected":true}`)}, nil
	}))
	_ = registry.Register(domain.NodePublish, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete}, nil
	}))
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := engine.ClaimAndExecute(context.Background(), "worker"); err == nil || !strings.Contains(err.Error(), "missing properties") {
			t.Fatalf("invalid runner output attempt %d error = %v", attempt, err)
		}
	}
	failed, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Nodes["transform"].Status != NodeFailed || failed.Status != RunFailed {
		t.Fatalf("invalid output did not fail closed: run=%s node=%s", failed.Status, failed.Nodes["transform"].Status)
	}
}

func conditionalDefinition(t *testing.T, userID string, now time.Time) domain.WorkflowDefinition {
	t.Helper()
	schema := engineSchema()
	condition := domain.NodeDefinition{ID: "condition", Name: "Condition", Type: domain.NodeCondition, InputSchema: schema, OutputPorts: map[string]domain.PortDefinition{"yes": {Schema: schema}, "no": {Schema: schema}}, Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{{Name: "yes", Expression: "true"}, {Name: "no", Default: true}}}}
	nodes := []domain.NodeDefinition{{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}}, condition, {ID: "yes", Name: "Yes", Type: domain.NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &domain.TransformNodeConfig{Transform: "yes"}}, {ID: "no", Name: "No", Type: domain.NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &domain.TransformNodeConfig{Transform: "no"}}, {ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}}}
	edges := []domain.WorkflowEdge{{ID: "e1", From: "input", To: "condition"}, {ID: "e2", From: "condition", FromPort: "yes", To: "yes"}, {ID: "e3", From: "condition", FromPort: "no", To: "no"}, {ID: "e4", From: "yes", To: "publish"}, {ID: "e5", From: "no", To: "publish"}}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Condition", "2", nodes, edges, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestConditionRoutesOneBranchAndCancelsTheOther(t *testing.T) {
	userID := uuid.NewString()
	definition := conditionalDefinition(t, userID, time.Now())
	registry := NewMapRegistry()
	_ = registry.Register(domain.NodeTransform, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete}, nil
	}))
	_ = registry.Register(domain.NodePublish, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete}, nil
	}))
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	engine.ConditionEvaluator = ConditionEvaluatorFunc(func(context.Context, Execution, []domain.ConditionBranch) (string, error) { return "yes", nil })
	run, _ := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	for index := 0; index < 3; index++ {
		if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
			t.Fatal(err)
		}
	}
	authorizeTestExecution(t, engine, run.ID, "publish", core.RoleOwner, core.ActionPublish)
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	final, _ := store.GetRun(context.Background(), run.ID)
	if final.Status != RunCompleted || final.Nodes["yes"].Status != NodeCompleted || final.Nodes["no"].Status != NodeCancelled {
		t.Fatalf("unexpected branch result: run=%s yes=%s no=%s", final.Status, final.Nodes["yes"].Status, final.Nodes["no"].Status)
	}
}

func fanOutDefinition(t *testing.T, userID string, now time.Time, policy domain.MergePolicy) domain.WorkflowDefinition {
	t.Helper()
	schema := engineSchema()
	nodes := []domain.NodeDefinition{{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactBlueprint}, MinimumArtifacts: 1}}, {ID: "fan", Name: "Fan", Type: domain.NodeFanOut, InputSchema: schema, OutputSchema: schema, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/payload/pages", SliceKeyPath: "/id", MergeNodeID: "merge", MaxParallel: 2}}, {ID: "work", Name: "Work", Type: domain.NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &domain.TransformNodeConfig{Transform: "work"}}, {ID: "merge", Name: "Merge", Type: domain.NodeMerge, InputSchema: schema, OutputSchema: schema, Merge: &domain.MergeNodeConfig{FanOutNodeID: "fan", Policy: policy}}, {ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}}}
	edges := []domain.WorkflowEdge{{ID: "e1", From: "input", To: "fan"}, {ID: "e2", From: "fan", To: "work"}, {ID: "e3", From: "work", To: "merge"}, {ID: "e4", From: "merge", To: "publish"}}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Fanout", "2", nodes, edges, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestFanOutCreatesSlicesHonorsParallelismAndMerges(t *testing.T) {
	userID := uuid.NewString()
	definition := fanOutDefinition(t, userID, time.Now(), domain.MergeAll)
	registry := NewMapRegistry()
	_ = registry.Register(domain.NodeFanOut, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		hash, _ := domain.CanonicalHash(map[string]any{"blueprint": true})
		ref := func() domain.ArtifactRef {
			return domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: hash}
		}
		return WorkerResult{Disposition: ResultComplete, FanOutItems: []FanOutItem{{Key: "a", Title: "A", Blueprint: ref()}, {Key: "b", Title: "B", Blueprint: ref()}, {Key: "c", Title: "C", Blueprint: ref()}}}, nil
	}))
	_ = registry.Register(domain.NodeTransform, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete}, nil
	}))
	_ = registry.Register(domain.NodePublish, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete}, nil
	}))
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	run, _ := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	afterFan, _ := store.GetRun(context.Background(), run.ID)
	ready := 0
	for _, node := range afterFan.Nodes {
		if node.SliceID != "" && node.Status == NodeReady {
			ready++
		}
	}
	if len(afterFan.Context.Slices) != 3 || ready != 2 {
		t.Fatalf("expected 3 slices and max 2 ready, got slices=%d ready=%d", len(afterFan.Context.Slices), ready)
	}
	for iteration := 0; iteration < 4; iteration++ {
		if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
			t.Fatal(err)
		}
	}
	authorizeTestExecution(t, engine, run.ID, "publish", core.RoleOwner, core.ActionPublish)
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	final, _ := store.GetRun(context.Background(), run.ID)
	if final.Status != RunCompleted || final.Nodes["merge"].Status != NodeCompleted {
		t.Fatalf("fanout workflow did not complete: %s merge=%s", final.Status, final.Nodes["merge"].Status)
	}
}

func TestReviewWaitSelfApprovalAndWaiver(t *testing.T) {
	schema := engineSchema()
	userID := uuid.NewString()
	nodes := []domain.NodeDefinition{{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}}, {ID: "review", Name: "Review", Type: domain.NodeReviewGate, InputSchema: schema, OutputSchema: schema, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true, AllowWaiver: true}}, {ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}}}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Review", "2", nodes, []domain.WorkflowEdge{{ID: "e1", From: "input", To: "review"}, {ID: "e2", From: "review", To: "publish"}}, userID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewMapRegistry()
	_ = registry.Register(domain.NodePublish, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete}, nil
	}))
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	run, _ := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	_ = engine.ClaimAndExecute(context.Background(), "worker")
	_ = engine.ClaimAndExecute(context.Background(), "worker")
	waiting, _ := store.GetRun(context.Background(), run.ID)
	if waiting.Status != RunWaitingReview {
		t.Fatalf("expected waiting review, got %s", waiting.Status)
	}
	gateErr := errors.New("canonical review is not approved")
	engine.ReviewGate = ReviewGateVerifierFunc(func(context.Context, string, []domain.ArtifactRef, domain.ReviewGateNodeConfig) error {
		return gateErr
	})
	if err := engine.ResolveReview(context.Background(), run.ID, "review", engineReviewDecision(uuid.NewString(), ReviewApprove, "")); !errors.Is(err, gateErr) {
		t.Fatalf("expected canonical review guard, got %v", err)
	}
	stillWaiting, _ := store.GetRun(context.Background(), run.ID)
	if stillWaiting.Nodes["review"].Status != NodeWaitingReview {
		t.Fatalf("failed canonical review changed node state: %s", stillWaiting.Nodes["review"].Status)
	}
	engine.ReviewGate = nil
	if err := engine.ResolveReview(context.Background(), run.ID, "review", engineReviewDecision(startedBy, ReviewApprove, "")); !errors.Is(err, domain.ErrSelfApproval) {
		t.Fatalf("expected self approval guard, got %v", err)
	}
	if err := engine.ResolveReview(context.Background(), run.ID, "review", engineReviewDecision(uuid.NewString(), ReviewWaive, "emergency")); err != nil {
		t.Fatal(err)
	}
	if err := engine.AuthorizeNodeExecution(context.Background(), run.ID, "publish", ActorProvenance{ActorID: uuid.NewString(), Role: core.RoleOwner, Action: core.ActionPublish, Source: ActorSourceAuthenticatedCommand, AuthorizedAt: time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	final, _ := store.GetRun(context.Background(), run.ID)
	if final.Status != RunCompleted || !final.Context.Nodes["review"].Waived {
		t.Fatalf("unexpected waiver result: %+v", final)
	}
}

func TestChangesRequestedReopensHumanEditBeforeReviewCanContinue(t *testing.T) {
	schema := engineSchema()
	userID := uuid.NewString()
	nodes := []domain.NodeDefinition{
		{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}},
		{ID: "edit", Name: "Edit", Type: domain.NodeHumanEdit, InputSchema: schema, OutputSchema: schema, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, RequiredRole: "editor"}},
		{ID: "review", Name: "Review", Type: domain.NodeReviewGate, InputSchema: schema, OutputSchema: schema, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true}},
		{ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}},
	}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Edit review", "2", nodes, []domain.WorkflowEdge{{ID: "e1", From: "input", To: "edit"}, {ID: "e2", From: "edit", To: "review"}, {ID: "e3", From: "review", To: "publish"}}, userID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewMapRegistry()
	_ = registry.Register(domain.NodePublish, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		return WorkerResult{Disposition: ResultComplete}, nil
	}))
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	refHash, _ := domain.CanonicalHash(map[string]any{"revision": 1})
	ref := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: refHash}
	output, _ := json.Marshal(map[string]any{"payload": map[string]any{}, "artifactRevision": ref})
	if err := engine.SubmitHumanInput(context.Background(), run.ID, "edit", output, startedBy); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ResolveReview(context.Background(), run.ID, "review", engineReviewDecision(uuid.NewString(), ReviewChanges, "Acceptance criteria are incomplete")); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != RunWaitingInput || reopened.Nodes["edit"].Status != NodeWaitingInput || reopened.Nodes["review"].Status != NodePending {
		t.Fatalf("changes did not reopen edit: run=%s edit=%s review=%s", reopened.Status, reopened.Nodes["edit"].Status, reopened.Nodes["review"].Status)
	}
}

func TestNodeTimeoutRetriesThenFails(t *testing.T) {
	schema := engineSchema()
	userID := uuid.NewString()
	nodes := []domain.NodeDefinition{{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}}, {ID: "build", Name: "Build", Type: domain.NodeWorkbenchBuild, InputSchema: schema, OutputSchema: schema, WorkbenchBuild: &domain.WorkbenchBuildNodeConfig{BuildManifestSchemaVersion: 1, MaxAttempts: 2, Timeout: 5 * time.Millisecond}}}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Timeout", "2", nodes, []domain.WorkflowEdge{{ID: "e1", From: "input", To: "build"}}, userID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewMapRegistry()
	_ = registry.Register(domain.NodeWorkbenchBuild, RunnerFunc(func(ctx context.Context, _ Execution) (WorkerResult, error) {
		<-ctx.Done()
		return WorkerResult{}, ctx.Err()
	}))
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	run, _ := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	_ = engine.ClaimAndExecute(context.Background(), "worker")
	if err := engine.ClaimAndExecute(context.Background(), "worker"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected final timeout, got %v", err)
	}
	final, _ := store.GetRun(context.Background(), run.ID)
	if final.Status != RunFailed || final.Nodes["build"].Attempt != 2 {
		t.Fatalf("expected failed after retries: %+v", final)
	}
	if err := engine.RetryNode(context.Background(), run.ID, "build", uuid.NewString(), "provider recovered"); err != nil {
		t.Fatal(err)
	}
	retried, _ := store.GetRun(context.Background(), run.ID)
	if retried.Status != RunRunning || retried.Nodes["build"].Status != NodeReady || retried.Nodes["build"].Attempt != 0 {
		t.Fatalf("manual retry did not resume run: %+v", retried.Nodes["build"])
	}
}
