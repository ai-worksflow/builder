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

type casInjectingStore struct {
	Store
	mu       sync.Mutex
	injected bool
	inject   func(context.Context) error
}

func (s *casInjectingStore) Commit(ctx context.Context, mutation RunMutation) error {
	s.mu.Lock()
	if s.injected {
		s.mu.Unlock()
		return s.Store.Commit(ctx, mutation)
	}
	s.injected = true
	inject := s.inject
	s.mu.Unlock()
	if inject != nil {
		if err := inject(ctx); err != nil {
			return err
		}
	}
	return ErrCASConflict
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
	manifest, err := domain.NewInputManifest(uuid.NewString(), projectID, "workflow_start", "", nil, []domain.ManifestSource{{Ref: source, Purpose: "project_brief"}}, json.RawMessage(`{"strict":true}`), "workflow-input/v1", userID, now)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

type startArtifactKindResolverFunc func(context.Context, domain.InputManifest) ([]string, error)

func (f startArtifactKindResolverFunc) ResolveStartArtifactKinds(ctx context.Context, manifest domain.InputManifest) ([]string, error) {
	return f(ctx, manifest)
}

type testRunnerRegistryWithFallback struct {
	primary  RunnerRegistry
	fallback map[domain.WorkflowNodeType]WorkerRunner
}

func (r testRunnerRegistryWithFallback) RunnerFor(nodeType domain.WorkflowNodeType) (WorkerRunner, bool) {
	if r.primary != nil {
		if runner, exists := r.primary.RunnerFor(nodeType); exists {
			return runner, true
		}
	}
	runner, exists := r.fallback[nodeType]
	return runner, exists
}

type testManifestCompilerFunc func(context.Context, Execution) (BuildManifest, error)

func (f testManifestCompilerFunc) Compile(ctx context.Context, execution Execution) (BuildManifest, error) {
	return f(ctx, execution)
}

// installCompleteTestExecutionProfileRuntime keeps isolated engine fixtures on
// the explicit legacy profile while still satisfying every binding declared by
// the sealed built-in descriptors. The fallback registry does not mutate the
// caller's registry, so tests may register their real node runners afterwards.
func installCompleteTestExecutionProfileRuntime(t *testing.T, engine *Engine, primary RunnerRegistry) {
	t.Helper()
	fallback := map[domain.WorkflowNodeType]WorkerRunner{}
	for nodeType, owner := range frozenV0V1NodeDispatchOwnership(CurrentWorkflowExecutionProfileDescriptor()) {
		if owner == workflowNodeDispatchRunner {
			fallback[nodeType] = RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
				return WorkerResult{Disposition: ResultComplete}, nil
			})
		}
	}
	engine.Runners = testRunnerRegistryWithFallback{primary: primary, fallback: fallback}
	if engine.ManifestCompilers == nil {
		engine.ManifestCompilers = NewBuildManifestRegistry()
	}
	capability := CurrentWorkflowExecutionProfileDescriptor().Capabilities.ManifestCompilers[0]
	key := manifestCompilerKey(capability.ManifestKind, capability.SchemaVersion, capability.Hook)
	if engine.ManifestCompilers.snapshot()[key] == nil {
		if err := engine.ManifestCompilers.Register(capability, testManifestCompilerFunc(func(context.Context, Execution) (BuildManifest, error) {
			return BuildManifest{}, errors.New("unexpected test manifest compiler execution")
		})); err != nil {
			t.Fatal(err)
		}
	}
}

func saveEngineDefinition(t *testing.T, store Store, projectID, userID string, definition domain.WorkflowDefinition) DefinitionRecord {
	t.Helper()
	if definition.ExecutionProfile.IsZero() {
		profile := LegacyWorkflowExecutionProfileRef()
		if definition.InputContract != nil && definition.OutputContract != nil {
			profile = CurrentWorkflowExecutionProfileRef()
		}
		var err error
		definition, err = definition.WithExecutionProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
	}
	record := DefinitionRecord{VersionID: uuid.NewString(), ProjectID: projectID, Key: "test", Title: definition.Name, Published: true, ExecutionProfile: definition.ExecutionProfile, Definition: definition}
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
	engine.RetryBackoff = func(int) time.Duration { return 0 }
	engine.StartArtifactKinds = startArtifactKindResolverFunc(func(context.Context, domain.InputManifest) ([]string, error) {
		return []string{"project_brief"}, nil
	})
	engine.HumanEditOutput = HumanEditOutputValidatorFunc(func(_ context.Context, execution Execution, output json.RawMessage, _ string) (HumanEditValidation, error) {
		refs, err := artifactRefsFromNodeOutput(output)
		if err != nil {
			return HumanEditValidation{}, err
		}
		if len(refs) == 0 {
			return HumanEditValidation{}, errors.New("missing artifact revision")
		}
		kind := execution.Definition.HumanEdit.ArtifactKind
		if kind == "" {
			switch execution.Definition.HumanEdit.ArtifactType {
			case domain.ArtifactDocument:
				kind = "project_brief"
			case domain.ArtifactBlueprint:
				kind = "blueprint"
			case domain.ArtifactPrototype:
				kind = "prototype"
			}
		}
		return HumanEditValidation{ArtifactRefs: refs, Primary: refs[len(refs)-1], ArtifactKind: kind}, nil
	})
	installCompleteTestExecutionProfileRuntime(t, engine, registry)
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

func TestEngineStartRejectsManifestDefinitionContractMismatchBeforeCreatingRun(t *testing.T) {
	t.Run("selection workflow with conversation manifest", func(t *testing.T) {
		userID := uuid.NewString()
		definition, err := blueprintSelectionFlowDefinitionForVersion(uuid.NewString(), 1, userID, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, nil)
		runID := uuid.NewString()
		_, err = engine.Start(context.Background(), StartRequest{
			RunID: runID, ProjectID: projectID, DefinitionVersionID: record.VersionID,
			InputManifest: manifest.Ref(), StartedBy: startedBy,
		})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("selection workflow mismatch error = %v", err)
		}
		if _, loadErr := store.GetRun(context.Background(), runID); !errors.Is(loadErr, domain.ErrNotFound) {
			t.Fatalf("incompatible start persisted a run: %v", loadErr)
		}
	})

	t.Run("selection manifest with ordinary workflow", func(t *testing.T) {
		userID := uuid.NewString()
		definition := simpleDefinition(t, uuid.NewString(), userID, time.Now().UTC())
		engine, store, clock, record, ordinaryManifest, projectID, startedBy := newTestEngine(t, definition, nil)
		selectionManifest, err := domain.NewInputManifest(
			uuid.NewString(), projectID, core.BlueprintSelectionJobType, "", nil,
			ordinaryManifest.Sources, json.RawMessage(`{"selectionId":"frozen"}`), "blueprint-selection/v1", startedBy, clock.Now(),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveManifest(context.Background(), selectionManifest); err != nil {
			t.Fatal(err)
		}
		runID := uuid.NewString()
		_, err = engine.Start(context.Background(), StartRequest{
			RunID: runID, ProjectID: projectID, DefinitionVersionID: record.VersionID,
			InputManifest: selectionManifest.Ref(), StartedBy: startedBy,
		})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("ordinary workflow mismatch error = %v", err)
		}
		if _, loadErr := store.GetRun(context.Background(), runID); !errors.Is(loadErr, domain.ErrNotFound) {
			t.Fatalf("incompatible start persisted a run: %v", loadErr)
		}
	})
}

func TestEngineStartReservesConversationIntentForAcceptedServerCommands(t *testing.T) {
	userID := uuid.NewString()
	definition := simpleDefinition(t, uuid.NewString(), userID, time.Now().UTC())
	engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, nil)
	reservedScope := json.RawMessage(`{"slice":"all","conversationIntent":{"proposalId":"server-reviewed"}}`)

	forgedRunID := uuid.NewString()
	_, err := engine.Start(context.Background(), StartRequest{
		RunID: forgedRunID, ProjectID: projectID, DefinitionVersionID: record.VersionID,
		InputManifest: manifest.Ref(), Scope: reservedScope, StartedBy: startedBy,
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("public start accepted forged reserved scope: %v", err)
	}
	if _, loadErr := store.GetRun(context.Background(), forgedRunID); !errors.Is(loadErr, domain.ErrNotFound) {
		t.Fatalf("forged reserved scope persisted a run: %v", loadErr)
	}

	ordinary, err := engine.Start(context.Background(), StartRequest{
		RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID,
		InputManifest: manifest.Ref(), Scope: json.RawMessage(`{"slice":"all"}`), StartedBy: startedBy,
	})
	if err != nil || string(ordinary.Scope) != `{"slice":"all"}` {
		t.Fatalf("ordinary workflow scope was rejected or changed: run=%+v err=%v", ordinary, err)
	}

	commandID := uuid.NewString()
	trusted, err := NewAcceptedConversationCommandStartRequest(commandID, record.VersionID, manifest.Ref(), reservedScope)
	if err != nil {
		t.Fatal(err)
	}
	trusted.ProjectID = projectID
	trusted.StartedBy = startedBy
	tampered := trusted
	tampered.Scope = json.RawMessage(`{"slice":"all","conversationIntent":{"proposalId":"changed-after-acceptance"}}`)
	if _, err := engine.Start(context.Background(), tampered); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("accepted provenance was not bound to the exact reviewed scope: %v", err)
	}
	trustedRun, err := engine.Start(context.Background(), trusted)
	if err != nil {
		t.Fatalf("accepted conversation command could not start: %v", err)
	}
	provenanceID, ok := trusted.AcceptedConversationCommandID()
	if !ok || provenanceID != commandID || trustedRun.ID != commandID || string(trustedRun.Scope) != string(reservedScope) {
		t.Fatalf("accepted command provenance/scope was not exact: provenance=%q ok=%t run=%+v", provenanceID, ok, trustedRun)
	}
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
			_, err := store.ClaimRunnable(context.Background(), fmt.Sprintf("worker-%d", worker), claimNow, time.Minute, run.ExecutionProfile)
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
	first, err := store.ClaimRunnable(context.Background(), "worker-1", clock.Now(), time.Second, run.ExecutionProfile)
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
			lease, err := store.ClaimRunnable(context.Background(), workerID, clock.Now(), time.Minute, run.ExecutionProfile)
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

func prepareTransformLease(t *testing.T, result WorkerResult, runnerErr error, leaseDuration time.Duration) (*Engine, *MemoryStore, *mutableClock, *RunRecord, Lease, *atomic.Int32) {
	t.Helper()
	userID := uuid.NewString()
	definition := simpleDefinition(t, uuid.NewString(), userID, time.Now())
	registry := NewMapRegistry()
	var calls atomic.Int32
	if err := registry.Register(domain.NodeTransform, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
		calls.Add(1)
		return result, runnerErr
	})); err != nil {
		t.Fatal(err)
	}
	engine, store, clock, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
	run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "setup-worker"); err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimRunnable(context.Background(), "outcome-worker", clock.Now(), leaseDuration, run.ExecutionProfile)
	if err != nil {
		t.Fatal(err)
	}
	if lease.NodeKey != "transform" {
		t.Fatalf("claimed node = %q, want transform", lease.NodeKey)
	}
	return engine, store, clock, run, lease, &calls
}

func commitConcurrentRunValue(ctx context.Context, store *MemoryStore, runID string, now time.Time) error {
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	run.Context.Values["concurrentCursorAdvance"] = json.RawMessage(`{"preserved":true}`)
	return store.Commit(ctx, RunMutation{
		RunID: run.ID, ExpectedCursor: run.EventCursor, Status: run.Status, Context: run.Context,
		Failure: run.Failure, CompletedAt: run.CompletedAt, CancelledAt: run.CancelledAt,
		Events:    []Event{{ID: uuid.NewString(), RunID: run.ID, Type: "test.concurrent_cursor_advanced", Payload: json.RawMessage(`{}`), CreatedAt: now}},
		UpdatedAt: now,
	})
}

func TestExecuteLeaseRebasesCachedOutcomeAfterRunCursorAdvance(t *testing.T) {
	runnerFailure := errors.New("cached runner failure")
	tests := []struct {
		name         string
		result       WorkerResult
		runnerErr    error
		priorFailure bool
		wantStatus   NodeStatus
	}{
		{name: "result", result: WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"payload":{}}`)}, priorFailure: true, wantStatus: NodeCompleted},
		{name: "failure", runnerErr: runnerFailure, wantStatus: NodeReady},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, store, clock, run, lease, calls := prepareTransformLease(t, test.result, test.runnerErr, time.Minute)
			if test.priorFailure {
				store.mu.Lock()
				store.runs[run.ID].Nodes["transform"].Failure = mustJSON(map[string]any{"message": "prior attempt failed", "attempt": 0})
				store.mu.Unlock()
			}
			engine.Store = &casInjectingStore{Store: store, inject: func(ctx context.Context) error {
				return commitConcurrentRunValue(ctx, store, run.ID, clock.Now())
			}}

			err := engine.ExecuteLease(context.Background(), lease)
			if test.runnerErr == nil && err != nil {
				t.Fatal(err)
			}
			if test.runnerErr != nil && !errors.Is(err, test.runnerErr) {
				t.Fatalf("execution error = %v, want %v", err, test.runnerErr)
			}
			if calls.Load() != 1 {
				t.Fatalf("runner calls = %d, want 1", calls.Load())
			}
			stored, err := store.GetRun(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			node := stored.Nodes["transform"]
			if node.Status != test.wantStatus || node.Attempt != 1 {
				t.Fatalf("rebased node = %+v", node)
			}
			if string(stored.Context.Values["concurrentCursorAdvance"]) != `{"preserved":true}` {
				t.Fatalf("concurrent context was overwritten: %s", stored.Context.Values["concurrentCursorAdvance"])
			}
			if test.runnerErr == nil && string(stored.Context.Nodes["transform"].Output) != `{"payload":{}}` {
				t.Fatalf("cached runner output was not committed: %s", stored.Context.Nodes["transform"].Output)
			}
			if test.runnerErr == nil && len(node.Failure) != 0 {
				t.Fatalf("successful CAS replay retained prior failure: %s", node.Failure)
			}
		})
	}
}

func TestExecuteLeaseCASRebaseRejectsLostLease(t *testing.T) {
	result := WorkerResult{Disposition: ResultComplete, Output: json.RawMessage(`{"payload":{}}`)}
	engine, store, clock, run, lease, calls := prepareTransformLease(t, result, nil, time.Second)
	var replacement Lease
	engine.Store = &casInjectingStore{Store: store, inject: func(ctx context.Context) error {
		clock.Advance(2 * time.Second)
		var err error
		replacement, err = store.ClaimRunnable(ctx, "replacement-worker", clock.Now(), time.Minute, run.ExecutionProfile)
		return err
	}}

	err := engine.ExecuteLease(context.Background(), lease)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("execution error = %v, want ErrLeaseLost", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("runner calls = %d, want 1", calls.Load())
	}
	stored, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	node := stored.Nodes["transform"]
	if replacement.NodeID != lease.NodeID || node.Status != NodeRunning || node.Attempt != 2 || node.LeaseOwner != "replacement-worker" {
		t.Fatalf("replacement lease was not preserved: lease=%+v node=%+v", replacement, node)
	}
	if len(stored.Context.Nodes["transform"].Output) != 0 {
		t.Fatalf("lost lease committed stale output: %s", stored.Context.Nodes["transform"].Output)
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
	retrying, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retrying.Nodes["transform"].Status != NodeReady || !strings.Contains(string(retrying.Nodes["transform"].Failure), "temporary") {
		t.Fatalf("automatic retry did not retain the current failure: %+v", retrying.Nodes["transform"])
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Nodes["transform"].Status != NodeCompleted || len(recovered.Nodes["transform"].Failure) != 0 {
		t.Fatalf("successful retry retained stale failure: %+v", recovered.Nodes["transform"])
	}
	authorizeTestExecution(t, engine, run.ID, "publish", core.RoleOwner, core.ActionPublish)
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	resumed, _ := store.GetRun(context.Background(), run.ID)
	if resumed.Status != RunCompleted || attempts.Load() != 2 {
		t.Fatalf("unexpected recovered run: %s attempts=%d", resumed.Status, attempts.Load())
	}
	if resumed.Definition != record.Definition.Ref() {
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

func TestFanOutItemLimitRejectsMaliciousRunnerBeforeInstantiation(t *testing.T) {
	for _, test := range []struct {
		name     string
		maxItems int
		count    int
	}{{name: "explicit", maxItems: 2, count: 3}, {name: "historical-default", maxItems: 0, count: domain.MaximumWorkflowFanOutItems + 1}} {
		t.Run(test.name, func(t *testing.T) {
			userID := uuid.NewString()
			base := fanOutDefinition(t, userID, time.Now(), domain.MergeAll)
			nodes := append([]domain.NodeDefinition(nil), base.Nodes...)
			for index := range nodes {
				if nodes[index].ID == "fan" {
					config := *nodes[index].FanOut
					config.MaxItems = test.maxItems
					nodes[index].FanOut = &config
				}
			}
			definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Bounded fanout", "2", nodes, base.Edges, userID, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			registry := NewMapRegistry()
			_ = registry.Register(domain.NodeFanOut, RunnerFunc(func(context.Context, Execution) (WorkerResult, error) {
				items := make([]FanOutItem, 0, test.count)
				hash, _ := domain.CanonicalHash(map[string]any{"bounded": true})
				for index := 0; index < test.count; index++ {
					items = append(items, FanOutItem{Key: fmt.Sprintf("item-%03d", index), Title: "Item", Blueprint: domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: hash}})
				}
				return WorkerResult{Disposition: ResultComplete, FanOutItems: items}, nil
			}))
			engine, store, _, record, manifest, projectID, startedBy := newTestEngine(t, definition, registry)
			run, err := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
				t.Fatal(err)
			}
			if err := engine.ClaimAndExecute(context.Background(), "worker"); err == nil || !strings.Contains(err.Error(), "maxItems") {
				t.Fatalf("over-limit runner result was not rejected: %v", err)
			}
			stored, err := store.GetRun(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored.Context.Slices) != 0 {
				t.Fatalf("over-limit fan-out partially instantiated %d slices", len(stored.Context.Slices))
			}
		})
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
	gateErr := errors.New("canonical review is not approved")
	engine.ReviewGate = ReviewGateVerifierFunc(func(context.Context, string, []domain.ArtifactRef, domain.ReviewGateNodeConfig) error {
		return gateErr
	})
	run, _ := engine.Start(context.Background(), StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(), StartedBy: startedBy})
	_ = engine.ClaimAndExecute(context.Background(), "worker")
	_ = engine.ClaimAndExecute(context.Background(), "worker")
	waiting, _ := store.GetRun(context.Background(), run.ID)
	if waiting.Status != RunWaitingReview {
		t.Fatalf("expected waiting review, got %s", waiting.Status)
	}
	if err := engine.ResolveReview(context.Background(), run.ID, "review", engineReviewDecision(uuid.NewString(), ReviewApprove, "")); !errors.Is(err, gateErr) {
		t.Fatalf("expected canonical review guard, got %v", err)
	}
	stillWaiting, _ := store.GetRun(context.Background(), run.ID)
	if stillWaiting.Nodes["review"].Status != NodeWaitingReview {
		t.Fatalf("failed canonical review changed node state: %s", stillWaiting.Nodes["review"].Status)
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
	// Requesting changes is not an approval. The workflow starter must be able
	// to reopen their own edit even when the gate prohibits self-approval.
	if err := engine.ResolveReview(context.Background(), run.ID, "review", engineReviewDecision(startedBy, ReviewChanges, "Acceptance criteria are incomplete")); err != nil {
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
	const definitionMaxAttempts = 2
	nodes := []domain.NodeDefinition{{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}}, {ID: "build", Name: "Build", Type: domain.NodeWorkbenchBuild, InputSchema: schema, OutputSchema: schema, WorkbenchBuild: &domain.WorkbenchBuildNodeConfig{BuildManifestSchemaVersion: 1, MaxAttempts: definitionMaxAttempts, Timeout: 5 * time.Millisecond}}}
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
	if retried.Context.Nodes["build"].MaxAttempts != definitionMaxAttempts {
		t.Fatalf("manual retry changed definition attempt budget: got %d want %d", retried.Context.Nodes["build"].MaxAttempts, definitionMaxAttempts)
	}
	for attempt := 1; attempt <= definitionMaxAttempts; attempt++ {
		if err := engine.ClaimAndExecute(context.Background(), "worker"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected timeout on manual retry attempt %d, got %v", attempt, err)
		}
		current, err := store.GetRun(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Nodes["build"].Attempt != attempt {
			t.Fatalf("manual retry attempt = %d, want %d", current.Nodes["build"].Attempt, attempt)
		}
		if current.Context.Nodes["build"].MaxAttempts != definitionMaxAttempts {
			t.Fatalf("manual retry budget drifted after attempt %d: got %d want %d", attempt, current.Context.Nodes["build"].MaxAttempts, definitionMaxAttempts)
		}
	}
	exhausted, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exhausted.Status != RunFailed || exhausted.Nodes["build"].Status != NodeFailed || exhausted.Nodes["build"].Attempt != definitionMaxAttempts {
		t.Fatalf("manual retry did not stop at the definition budget: run=%s node=%+v", exhausted.Status, exhausted.Nodes["build"])
	}
}
