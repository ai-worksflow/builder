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

type fixedActionProjectionGovernance struct {
	value core.ProjectGovernance
}

func (r fixedActionProjectionGovernance) Governance(context.Context, string) (core.ProjectGovernance, error) {
	return r.value, nil
}

type actionFenceFixture struct {
	ctx       context.Context
	store     *MemoryStore
	engine    *Engine
	facade    Facade
	run       *RunRecord
	projectID string
	actorID   string
	nodeKey   string
}

func newProfileV3ActionFenceFixture(t *testing.T, nodeKey string, status NodeStatus) actionFenceFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	definition := profileV3RuntimeFenceDefinition(t, now)
	definitionNode, exists := definition.FindNode(nodeKey)
	if !exists {
		t.Fatalf("definition node %q is unavailable", nodeKey)
	}
	projectID, versionID, runID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	if err := store.SaveDefinition(ctx, DefinitionRecord{
		VersionID: versionID, ProjectID: projectID, Key: "action-fence", Title: definition.Name,
		Published: true, ExecutionProfile: WorkflowExecutionProfileV3Ref(), Definition: definition,
	}); err != nil {
		t.Fatal(err)
	}
	runContext := NewRunContext()
	runContext.Nodes[nodeKey] = NodeMetadata{DefinitionNodeID: nodeKey, MaxAttempts: 1}
	node := &NodeRecord{
		ID: uuid.NewString(), RunID: runID, Key: nodeKey, DefinitionNodeID: nodeKey,
		Type: definitionNode.Type, Status: status, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if status == NodeRunning {
		expiresAt := now.Add(time.Minute)
		node.Attempt = 1
		node.LeaseOwner = "forged-generic-worker"
		node.LeaseExpiresAt = &expiresAt
	}
	run := &RunRecord{
		ID: runID, ProjectID: projectID, DefinitionVersionID: versionID,
		Definition:       definition.RefForExecutionProfile(WorkflowExecutionProfileV3Ref()),
		ExecutionProfile: WorkflowExecutionProfileV3Ref(), Status: RunRunning,
		GovernanceMode: core.GovernanceModeSolo, Scope: json.RawMessage(`{}`), Context: runContext,
		StartedBy: actorID, StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*NodeRecord{nodeKey: node},
	}
	if err := store.CreateRun(ctx, run, []Event{{
		ID: uuid.NewString(), RunID: runID, Type: "run.started", Payload: json.RawMessage(`{}`), CreatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	engine.Clock = fixedClockV3Test{value: now}
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	installProfileV3RuntimeFenceBundle(t, engine)
	return actionFenceFixture{
		ctx: ctx, store: store, engine: engine,
		facade: Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{role: core.RoleOwner}},
		run:    stored, projectID: projectID, actorID: actorID, nodeKey: nodeKey,
	}
}

func assertQualifiedReleaseFence(t *testing.T, fixture actionFenceFixture, status NodeStatus, command func() error) {
	t.Helper()
	err := command()
	if !errors.Is(err, domain.ErrInvalidTransition) || !strings.Contains(err.Error(), "qualified Release Controller") {
		t.Fatalf("generic qualified release command error = %v", err)
	}
	stored, loadErr := fixture.store.GetRun(fixture.ctx, fixture.run.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if stored.EventCursor != fixture.run.EventCursor || stored.Nodes[fixture.nodeKey].Status != status {
		t.Fatalf("rejected command mutated run: cursor=%d node=%+v", stored.EventCursor, stored.Nodes[fixture.nodeKey])
	}
}

func TestEngineRejectsEveryGenericProfileV3PublishEntryPoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  NodeStatus
		command func(actionFenceFixture) error
	}{
		{
			name: "authorize", status: NodeWaitingInput,
			command: func(f actionFenceFixture) error {
				return f.engine.AuthorizeNodeExecution(f.ctx, f.run.ID, f.nodeKey, ActorProvenance{
					ActorID: f.actorID, Role: core.RoleOwner, Action: core.ActionPublish,
					Source: ActorSourceAuthenticatedCommand, AuthorizedAt: f.engine.now(),
				})
			},
		},
		{
			name: "submit input", status: NodeWaitingInput,
			command: func(f actionFenceFixture) error {
				return f.engine.SubmitHumanInput(f.ctx, f.run.ID, f.nodeKey, json.RawMessage(`{}`), f.actorID)
			},
		},
		{
			name: "record proposal", status: NodeWaitingInput,
			command: func(f actionFenceFixture) error {
				return f.engine.RecordProposal(f.ctx, f.run.ID, f.nodeKey, domain.ProposalRef{}, f.actorID)
			},
		},
		{
			name: "resolve review", status: NodeWaitingReview,
			command: func(f actionFenceFixture) error {
				return f.engine.ResolveReview(f.ctx, f.run.ID, f.nodeKey, ReviewDecision{
					Resolution: ReviewApprove,
					Actor: ActorProvenance{
						ActorID: f.actorID, Role: core.RoleOwner, Action: core.ActionApprove,
						Source: ActorSourceAuthenticatedCommand, AuthorizedAt: f.engine.now(),
					},
				})
			},
		},
		{
			name: "waive", status: NodeFailed,
			command: func(f actionFenceFixture) error {
				return f.engine.WaiveNode(f.ctx, f.run.ID, f.nodeKey, ActorProvenance{
					ActorID: f.actorID, Role: core.RoleOwner, Action: core.ActionApprove,
					Source: ActorSourceAuthenticatedCommand, AuthorizedAt: f.engine.now(),
				}, "operator waiver")
			},
		},
		{
			name: "retry", status: NodeFailed,
			command: func(f actionFenceFixture) error {
				return f.engine.RetryNode(f.ctx, f.run.ID, f.nodeKey, f.actorID, "manual retry")
			},
		},
		{
			name: "execute forged lease", status: NodeRunning,
			command: func(f actionFenceFixture) error {
				node := f.run.Nodes[f.nodeKey]
				return f.engine.ExecuteLease(f.ctx, Lease{
					RunID: f.run.ID, NodeID: node.ID, NodeKey: node.Key, WorkerID: node.LeaseOwner,
					Attempt: node.Attempt, LeaseExpiresAt: *node.LeaseExpiresAt,
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProfileV3ActionFenceFixture(t, "publish", test.status)
			assertQualifiedReleaseFence(t, fixture, test.status, func() error { return test.command(fixture) })
		})
	}
}

func TestFacadeRejectsEveryGenericProfileV3PublishCommand(t *testing.T) {
	t.Parallel()
	commands := map[string]func(actionFenceFixture) error{
		"resume": func(f actionFenceFixture) error {
			return f.facade.Resume(f.ctx, f.projectID, f.run.ID, f.nodeKey, f.actorID, json.RawMessage(`{}`))
		},
		"authorize": func(f actionFenceFixture) error {
			return f.facade.AuthorizeExecution(f.ctx, f.projectID, f.run.ID, f.nodeKey, f.actorID)
		},
		"record proposal": func(f actionFenceFixture) error {
			return f.facade.RecordProposal(f.ctx, f.projectID, f.run.ID, f.nodeKey, f.actorID, domain.ProposalRef{})
		},
		"resolve review": func(f actionFenceFixture) error {
			return f.facade.ResolveReview(f.ctx, f.projectID, f.run.ID, f.nodeKey, f.actorID, ReviewApprove, "approved", false)
		},
		"retry": func(f actionFenceFixture) error {
			return f.facade.Retry(f.ctx, f.projectID, f.run.ID, f.nodeKey, f.actorID, "manual retry")
		},
		"waive": func(f actionFenceFixture) error {
			return f.facade.Waive(f.ctx, f.projectID, f.run.ID, f.nodeKey, f.actorID, "operator waiver")
		},
	}
	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			fixture := newProfileV3ActionFenceFixture(t, "publish", NodeWaitingInput)
			assertQualifiedReleaseFence(t, fixture, NodeWaitingInput, func() error { return command(fixture) })
		})
	}
}

func TestProjectRunNodeActionsUsesWorkbenchSubmitAndClosesDedicatedNodes(t *testing.T) {
	t.Parallel()
	workbench := newProfileV3ActionFenceFixture(t, "workbench", NodeWaitingInput)
	actions, err := workbench.facade.ProjectRunNodeActions(
		workbench.ctx, workbench.projectID, workbench.run.ID, workbench.actorID, workbench.run.EventCursor,
	)
	if err != nil {
		t.Fatal(err)
	}
	projection := actions["workbench"]
	if len(projection.AllowedActions) != 1 || projection.AllowedActions[0] != WorkflowNodeActionSubmitInput {
		t.Fatalf("Workbench actions = %+v, want submit_input only", projection)
	}

	external := newProfileV3ActionFenceFixture(t, "external-qualification", NodeWaitingReview)
	actions, err = external.facade.ProjectRunNodeActions(
		external.ctx, external.projectID, external.run.ID, external.actorID, external.run.EventCursor,
	)
	if err != nil {
		t.Fatal(err)
	}
	projection = actions["external-qualification"]
	if len(projection.AllowedActions) != 0 || len(projection.BlockingReasons) != 1 ||
		projection.BlockingReasons[0].Code != "external_qualification_authority_required" {
		t.Fatalf("dedicated qualification actions = %+v", projection)
	}
}

func TestProjectRunNodeActionsMirrorsSelfReviewPolicy(t *testing.T) {
	t.Parallel()
	for _, governance := range []core.ProjectGovernance{
		{Mode: core.GovernanceModeTeam, OwnerCount: 1},
		{Mode: core.GovernanceModeSolo, OwnerCount: 1},
	} {
		governance := governance
		t.Run(string(governance.Mode), func(t *testing.T) {
			fixture := newSelfReviewActionFixture(t, governance)
			actions, err := fixture.facade.ProjectRunNodeActions(
				fixture.ctx, fixture.projectID, fixture.run.ID, fixture.actorID, fixture.run.EventCursor,
			)
			if err != nil {
				t.Fatal(err)
			}
			projection := actions[fixture.nodeKey]
			hasApprove, hasWaive := false, false
			for _, action := range projection.AllowedActions {
				hasApprove = hasApprove || action == WorkflowNodeActionApproveReview
				hasWaive = hasWaive || action == WorkflowNodeActionWaiveReview
			}
			if governance.Mode == core.GovernanceModeSolo && !hasApprove {
				t.Fatalf("Solo sole Owner approval was not projected: %+v", projection)
			}
			if governance.Mode == core.GovernanceModeTeam && hasApprove {
				t.Fatalf("team self-approval was projected: %+v", projection)
			}
			if hasWaive {
				t.Fatalf("self-waiver was projected: %+v", projection)
			}
		})
	}
}

func newSelfReviewActionFixture(t *testing.T, governance core.ProjectGovernance) actionFenceFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	schema := json.RawMessage(`{"type":"object"}`)
	actorID := uuid.NewString()
	definition, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Self review projection", "2",
		[]domain.NodeDefinition{
			{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}},
			{ID: "review", Name: "Review", Type: domain.NodeReviewGate, InputSchema: schema, OutputSchema: schema, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true, AllowWaiver: true}},
			{ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}},
		},
		[]domain.WorkflowEdge{{ID: "input-review", From: "input", To: "review"}, {ID: "review-publish", From: "review", To: "publish"}},
		actorID,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	projectID := uuid.NewString()
	store := NewMemoryStore(nil)
	record := saveEngineDefinition(t, store, projectID, actorID, definition)
	runID := uuid.NewString()
	runContext := NewRunContext()
	runContext.Nodes["review"] = NodeMetadata{DefinitionNodeID: "review", MaxAttempts: 1}
	run := &RunRecord{
		ID: runID, ProjectID: projectID, DefinitionVersionID: record.VersionID,
		Definition: record.Definition.RefForExecutionProfile(record.ExecutionProfile), ExecutionProfile: record.ExecutionProfile,
		Status: RunWaitingReview, GovernanceMode: governance.Mode, Scope: json.RawMessage(`{}`), Context: runContext,
		StartedBy: actorID, StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*NodeRecord{"review": {
			ID: uuid.NewString(), RunID: runID, Key: "review", DefinitionNodeID: "review", Type: domain.NodeReviewGate,
			Status: NodeWaitingReview, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
		}},
	}
	if err := store.CreateRun(ctx, run, []Event{{ID: uuid.NewString(), RunID: runID, Type: "run.started", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	return actionFenceFixture{
		ctx: ctx, store: store, engine: engine,
		facade: Facade{
			Engine: engine, Store: store, Access: fixedWorkflowAccess{role: core.RoleOwner},
			Governance: fixedActionProjectionGovernance{value: governance},
		},
		run: stored, projectID: projectID, actorID: actorID, nodeKey: "review",
	}
}
