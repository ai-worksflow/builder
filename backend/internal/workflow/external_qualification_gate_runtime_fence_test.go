package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestExternalQualificationGateRejectsGenericControlPlane(t *testing.T) {
	t.Parallel()
	external := domain.NodeDefinition{Type: domain.NodeExternalQualificationGate}
	err := rejectGenericExternalQualificationControl(external)
	if !errors.Is(err, domain.ErrInvalidTransition) || !strings.Contains(err.Error(), "canonical qualification authority protocols") {
		t.Fatalf("dedicated gate control error = %v", err)
	}
	if err := rejectGenericExternalQualificationControl(domain.NodeDefinition{Type: domain.NodeQualityGate}); err != nil {
		t.Fatalf("ordinary workflow node was rejected: %v", err)
	}
}

func TestMemoryStoreNeverLeasesExternalQualificationGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 17, 0, 0, 0, time.UTC)
	definition := profileV3RuntimeFenceDefinition(t, now)
	projectID, versionID, runID, nodeID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	record := DefinitionRecord{
		VersionID: versionID, ProjectID: projectID, Key: "qualification-fence", Title: definition.Name,
		Published: true, ExecutionProfile: WorkflowExecutionProfileV3Ref(), Definition: definition,
	}
	if err := store.SaveDefinition(ctx, record); err != nil {
		t.Fatal(err)
	}
	runContext := NewRunContext()
	runContext.Nodes["external-qualification"] = NodeMetadata{DefinitionNodeID: "external-qualification", MaxAttempts: 1}
	run := &RunRecord{
		ID: runID, ProjectID: projectID, DefinitionVersionID: versionID,
		Definition:       definition.RefForExecutionProfile(WorkflowExecutionProfileV3Ref()),
		ExecutionProfile: WorkflowExecutionProfileV3Ref(), Status: RunRunning,
		GovernanceMode: core.GovernanceModeSolo, Scope: []byte(`{}`), Context: runContext,
		StartedBy: uuid.NewString(), StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*NodeRecord{
			"external-qualification": {
				ID: nodeID, RunID: runID, Key: "external-qualification", DefinitionNodeID: "external-qualification",
				Type: domain.NodeExternalQualificationGate, Status: NodeReady,
				AvailableAt: now.Add(-time.Minute), CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	if err := store.CreateRun(ctx, run, []Event{{ID: uuid.NewString(), RunID: runID, Type: "run.started", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnable(ctx, "generic-worker", now, time.Minute, WorkflowExecutionProfileV3Ref()); !errors.Is(err, ErrNoRunnableNode) {
		t.Fatalf("dedicated external qualification gate was leased: %v", err)
	}
	stored, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Nodes["external-qualification"].Status != NodeReady || stored.Nodes["external-qualification"].Attempt != 0 || stored.EventCursor != 1 {
		t.Fatalf("rejected lease mutated the dedicated gate: node=%+v cursor=%d", stored.Nodes["external-qualification"], stored.EventCursor)
	}
}

func TestEngineRejectsForgedExternalQualificationLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 17, 15, 0, 0, time.UTC)
	definition := profileV3RuntimeFenceDefinition(t, now)
	projectID, versionID, runID, nodeID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	if err := store.SaveDefinition(ctx, DefinitionRecord{
		VersionID: versionID, ProjectID: projectID, Key: "qualification-execution-fence", Title: definition.Name,
		Published: true, ExecutionProfile: WorkflowExecutionProfileV3Ref(), Definition: definition,
	}); err != nil {
		t.Fatal(err)
	}
	runContext := NewRunContext()
	runContext.Nodes["external-qualification"] = NodeMetadata{DefinitionNodeID: "external-qualification", MaxAttempts: 1}
	leaseExpiresAt := now.Add(time.Minute)
	run := &RunRecord{
		ID: runID, ProjectID: projectID, DefinitionVersionID: versionID,
		Definition:       definition.RefForExecutionProfile(WorkflowExecutionProfileV3Ref()),
		ExecutionProfile: WorkflowExecutionProfileV3Ref(), Status: RunRunning,
		GovernanceMode: core.GovernanceModeSolo, Scope: []byte(`{}`), Context: runContext,
		StartedBy: uuid.NewString(), StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*NodeRecord{
			"external-qualification": {
				ID: nodeID, RunID: runID, Key: "external-qualification", DefinitionNodeID: "external-qualification",
				Type: domain.NodeExternalQualificationGate, Status: NodeRunning, Attempt: 1,
				LeaseOwner: "forged-worker", LeaseExpiresAt: &leaseExpiresAt, CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	if err := store.CreateRun(ctx, run, []Event{{ID: uuid.NewString(), RunID: runID, Type: "run.started", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	installProfileV3RuntimeFenceBundle(t, engine)
	err = engine.ExecuteLease(ctx, Lease{
		RunID: runID, NodeID: nodeID, NodeKey: "external-qualification", WorkerID: "forged-worker",
		Attempt: 1, LeaseExpiresAt: leaseExpiresAt,
	})
	if !errors.Is(err, domain.ErrInvalidTransition) || !strings.Contains(err.Error(), "canonical qualification authority protocols") {
		t.Fatalf("forged generic lease error = %v", err)
	}
	stored, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if node := stored.Nodes["external-qualification"]; node.Status != NodeRunning || node.Attempt != 1 || node.LeaseOwner != "forged-worker" || stored.EventCursor != 1 {
		t.Fatalf("rejected execution mutated the dedicated gate: node=%+v cursor=%d", node, stored.EventCursor)
	}
}

func installProfileV3RuntimeFenceBundle(t *testing.T, engine *Engine) {
	t.Helper()
	descriptor := WorkflowExecutionProfileV3Descriptor()
	dispatch := frozenV0V1NodeDispatchOwnership(descriptor)
	dispatch[domain.NodeExternalQualificationGate] = workflowNodeDispatchInterpreter
	bundle := WorkflowExecutionProfileBundle{
		Descriptor: descriptor, componentIdentity: descriptor.Components, nodeDispatch: dispatch,
		runtimeFactory: func(bootstrap workflowExecutionRuntime, _ WorkflowExecutionProfileDescriptor) (workflowExecutionRuntime, error) {
			runtime := cloneExecutionRuntime(bootstrap)
			runtime.conditionEvaluator = DeclarativeConditionEvaluatorV1{}
			return runtime, nil
		},
		buildInputs: buildNodeInputEnvelopeV2, executeNodeFn: executeNodeV2,
		validateResultFn: validateResultV2, applyResultFn: applyResultV2, reconcileFn: reconcileV2,
	}
	if err := engine.ExecutionProfiles.Register(bundle); err != nil {
		t.Fatal(err)
	}
}

func TestFacadeRejectsGenericExternalQualificationCommands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 17, 30, 0, 0, time.UTC)
	definition := profileV3RuntimeFenceDefinition(t, now)
	projectID, versionID, runID, nodeID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	if err := store.SaveDefinition(ctx, DefinitionRecord{
		VersionID: versionID, ProjectID: projectID, Key: "qualification-facade-fence", Title: definition.Name,
		Published: true, ExecutionProfile: WorkflowExecutionProfileV3Ref(), Definition: definition,
	}); err != nil {
		t.Fatal(err)
	}
	runContext := NewRunContext()
	runContext.Nodes["external-qualification"] = NodeMetadata{DefinitionNodeID: "external-qualification", MaxAttempts: 1}
	run := &RunRecord{
		ID: runID, ProjectID: projectID, DefinitionVersionID: versionID,
		Definition:       definition.RefForExecutionProfile(WorkflowExecutionProfileV3Ref()),
		ExecutionProfile: WorkflowExecutionProfileV3Ref(), Status: RunRunning,
		GovernanceMode: core.GovernanceModeSolo, Scope: []byte(`{}`), Context: runContext,
		StartedBy: actorID, StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*NodeRecord{
			"external-qualification": {
				ID: nodeID, RunID: runID, Key: "external-qualification", DefinitionNodeID: "external-qualification",
				Type: domain.NodeExternalQualificationGate, Status: NodeWaitingQualification,
				InputAuthorityID: uuid.NewString(), CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	if err := store.CreateRun(ctx, run, []Event{{ID: uuid.NewString(), RunID: runID, Type: "run.started", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{role: core.RoleOwner}}
	commands := map[string]func() error{
		"resume": func() error {
			return facade.Resume(ctx, projectID, runID, "external-qualification", actorID, json.RawMessage(`{}`))
		},
		"authorize execution": func() error {
			return facade.AuthorizeExecution(ctx, projectID, runID, "external-qualification", actorID)
		},
		"record proposal": func() error {
			return facade.RecordProposal(ctx, projectID, runID, "external-qualification", actorID, domain.ProposalRef{})
		},
		"resolve review": func() error {
			return facade.ResolveReview(ctx, projectID, runID, "external-qualification", actorID, ReviewApprove, "approved", false)
		},
		"retry": func() error {
			return facade.Retry(ctx, projectID, runID, "external-qualification", actorID, "retry")
		},
		"waive": func() error {
			return facade.Waive(ctx, projectID, runID, "external-qualification", actorID, "waive")
		},
	}
	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			err := command()
			if !errors.Is(err, domain.ErrInvalidTransition) || !strings.Contains(err.Error(), "canonical qualification authority protocols") {
				t.Fatalf("generic command error = %v", err)
			}
		})
	}
}

func profileV3RuntimeFenceDefinition(t *testing.T, now time.Time) domain.WorkflowDefinition {
	t.Helper()
	fixture := governedProfileV3Definition(t)
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		uuid.NewString(), 1, fixture.Name, fixture.SchemaVersion,
		fixture.Nodes, fixture.Edges, *fixture.InputContract, *fixture.OutputContract,
		WorkflowExecutionProfileV3Ref(), uuid.NewString(), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}
