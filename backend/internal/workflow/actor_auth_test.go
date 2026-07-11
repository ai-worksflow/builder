package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type actorTestAccess struct {
	roles map[string]core.Role
}

func (a actorTestAccess) Authorize(_ context.Context, _, actorID string, action core.Action) (core.Role, error) {
	role, exists := a.roles[actorID]
	if !exists {
		return "", core.ErrNotFound
	}
	allowed := action == core.ActionView
	switch role {
	case core.RoleOwner, core.RoleAdmin:
		allowed = true
	case core.RoleEditor:
		allowed = allowed || action == core.ActionEdit || action == core.ActionReview
	case core.RoleCommenter:
		allowed = allowed || action == core.ActionComment
	}
	if !allowed {
		return role, core.ErrForbidden
	}
	return role, nil
}

type actorBuildManifestHook struct {
	source domain.ArtifactRef
}

func (h actorBuildManifestHook) Compile(_ context.Context, execution Execution) (BuildManifest, error) {
	manifest := BuildManifest{
		SchemaVersion: 1, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID,
		ManifestGroupKey: uuid.NewString(),
		SliceIDs:         []string{"slice"}, BundleIDs: []string{"bundle"}, Sources: []domain.ArtifactRef{h.source},
		Constraints: json.RawMessage(`{}`), CreatedAt: time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC),
	}
	return manifest, manifest.Freeze()
}

func actorSecurityDefinition(t *testing.T, createdBy string, withReview bool) domain.WorkflowDefinition {
	t.Helper()
	schema := json.RawMessage(`{"type":"object"}`)
	nodes := []domain.NodeDefinition{
		{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}},
		{ID: "manifest", Name: "Manifest", Type: domain.NodeManifestCompiler, InputSchema: schema, OutputSchema: schema, ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application", SchemaVersion: 1, Hook: "test"}},
	}
	edges := []domain.WorkflowEdge{{ID: "input-manifest", From: "input", To: "manifest"}}
	if withReview {
		nodes = append(nodes,
			domain.NodeDefinition{ID: "quality", Name: "Quality", Type: domain.NodeQualityGate, InputSchema: schema, OutputSchema: schema, QualityGate: &domain.QualityGateNodeConfig{GateName: "release", Blocking: true, RequiredRole: "editor"}},
			domain.NodeDefinition{ID: "review", Name: "Release review", Type: domain.NodeReviewGate, InputSchema: schema, OutputSchema: schema, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "admin", MinimumApprovals: 1}},
			domain.NodeDefinition{ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "admin"}},
		)
		edges = append(edges,
			domain.WorkflowEdge{ID: "manifest-quality", From: "manifest", To: "quality"},
			domain.WorkflowEdge{ID: "quality-review", From: "quality", To: "review"},
			domain.WorkflowEdge{ID: "review-publish", From: "review", To: "publish"},
		)
	} else {
		nodes = append(nodes,
			domain.NodeDefinition{ID: "quality", Name: "Quality", Type: domain.NodeQualityGate, InputSchema: schema, OutputSchema: schema, QualityGate: &domain.QualityGateNodeConfig{GateName: "release", Blocking: true, RequiredRole: "editor"}},
			domain.NodeDefinition{ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: "admin"}},
		)
		edges = append(edges,
			domain.WorkflowEdge{ID: "manifest-quality", From: "manifest", To: "quality"},
			domain.WorkflowEdge{ID: "quality-publish", From: "quality", To: "publish"},
		)
	}
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, "Actor security", "2", nodes, edges, createdBy, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func actorSecurityRuntime(t *testing.T, definition domain.WorkflowDefinition, editorID, adminID string) (*Facade, *Engine, *MemoryStore, domain.InputManifest, DefinitionRecord) {
	t.Helper()
	projectID := uuid.NewString()
	store := NewMemoryStore(nil)
	record := saveEngineDefinition(t, store, projectID, adminID, definition)
	manifest := testManifestForEngine(t, projectID, editorID, time.Now().UTC())
	if err := store.SaveManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	hook := actorBuildManifestHook{source: manifest.Sources[0].Ref}
	engine.BuildManifestHook = hook
	engine.ManifestCompilers = NewBuildManifestRegistry()
	for _, capability := range []ManifestCompilerCapability{
		CurrentWorkflowExecutionProfileDescriptor().Capabilities.ManifestCompilers[0],
		{ManifestKind: "application", SchemaVersion: 1, Hook: "test"},
	} {
		if err := engine.ManifestCompilers.Register(capability, hook); err != nil {
			t.Fatal(err)
		}
	}
	access := actorTestAccess{roles: map[string]core.Role{editorID: core.RoleEditor, adminID: core.RoleAdmin}}
	facade := &Facade{Engine: engine, Store: store, Access: access}
	return facade, engine, store, manifest, record
}

func TestPrivilegedNodesUseAuthenticatedActorNotRunStarterOrOutput(t *testing.T) {
	editorID, adminID := uuid.NewString(), uuid.NewString()
	definition := actorSecurityDefinition(t, adminID, false)
	facade, engine, store, manifest, record := actorSecurityRuntime(t, definition, editorID, adminID)
	qualityActor, publishActor := "", ""
	registry := NewMapRegistry()
	if err := registry.Register(domain.NodeQualityGate, QualityGateRunner{Access: facade.Access, Evaluator: QualityEvaluatorFunc(func(_ context.Context, execution Execution) (QualityResult, error) {
		actor, err := execution.ExecutionActor()
		if err == nil {
			qualityActor = actor.ActorID
		}
		workspace := manifest.Sources[0].Ref
		return QualityResult{Passed: true, QualityRunID: uuid.NewString(), WorkspaceRevision: &workspace}, err
	})}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(domain.NodePublish, PublishRunner{Access: facade.Access, Publisher: PublisherFunc(func(_ context.Context, _, _, actorID, _ string, _ WorkflowPublishInput) (PublishResult, error) {
		publishActor = actorID
		return PublishResult{URL: "/published/preview", DeploymentID: uuid.NewString()}, nil
	})}); err != nil {
		t.Fatal(err)
	}
	installCompleteTestExecutionProfileRuntime(t, engine, registry)
	run, err := facade.Start(context.Background(), manifest.ProjectID, editorID, StartRequest{RunID: uuid.NewString(), DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	waiting, _ := store.GetRun(context.Background(), run.ID)
	if waiting.Nodes["quality"].Status != NodeWaitingInput || waiting.Context.Nodes["quality"].ExecutionActor != nil {
		t.Fatalf("quality did not stop for actor authorization: %+v", waiting.Nodes["quality"])
	}
	forgedOutput := json.RawMessage(`{"executionActor":{"actorId":"` + adminID + `","role":"admin","action":"publish","source":"authenticated_command"}}`)
	if err := facade.Resume(context.Background(), manifest.ProjectID, run.ID, "quality", editorID, forgedOutput); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("forged actor output was not rejected: %v", err)
	}
	unchanged, _ := store.GetRun(context.Background(), run.ID)
	if unchanged.Context.Nodes["quality"].ExecutionActor != nil {
		t.Fatal("user-controlled output populated execution actor provenance")
	}
	if err := facade.AuthorizeExecution(context.Background(), manifest.ProjectID, run.ID, "quality", editorID); err != nil {
		t.Fatal(err)
	}
	roles := facade.Access.(actorTestAccess).roles
	roles[editorID] = core.RoleViewer
	engine.RetryBackoff = func(int) time.Duration { return 0 }
	if err := engine.ClaimAndExecute(context.Background(), "worker"); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("quality execution ignored revoked role: %v", err)
	}
	roles[editorID] = core.RoleEditor
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if qualityActor != editorID {
		t.Fatalf("quality actor=%q want authenticated editor=%q", qualityActor, editorID)
	}
	if err := facade.AuthorizeExecution(context.Background(), manifest.ProjectID, run.ID, "publish", editorID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("editor authorized publish: %v", err)
	}
	if err := facade.AuthorizeExecution(context.Background(), manifest.ProjectID, run.ID, "publish", adminID); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	completed, _ := store.GetRun(context.Background(), run.ID)
	if completed.Status != RunCompleted || publishActor != adminID || publishActor == completed.StartedBy {
		t.Fatalf("publish attribution failed: status=%s actor=%q starter=%q", completed.Status, publishActor, completed.StartedBy)
	}
	qualityProvenance := completed.Context.Nodes["quality"].ExecutionActor
	publishProvenance := completed.Context.Nodes["publish"].ExecutionActor
	if qualityProvenance == nil || qualityProvenance.ActorID != editorID || qualityProvenance.Action != core.ActionEdit || publishProvenance == nil || publishProvenance.ActorID != adminID || publishProvenance.Action != core.ActionPublish {
		t.Fatalf("execution provenance was not persisted: quality=%+v publish=%+v", qualityProvenance, publishProvenance)
	}
	events, err := store.ListEvents(context.Background(), run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertActorEvent(t, events, "node.execution_authorized", "publish", adminID)
	assertActorEvent(t, events, "node.completed", "publish", adminID)
}

func TestReviewApprovalAtomicallyHandsOffAuthorizedPublishActor(t *testing.T) {
	editorID, adminID := uuid.NewString(), uuid.NewString()
	definition := actorSecurityDefinition(t, adminID, true)
	facade, engine, store, manifest, record := actorSecurityRuntime(t, definition, editorID, adminID)
	publishActor := ""
	registry := NewMapRegistry()
	if err := registry.Register(domain.NodeQualityGate, QualityGateRunner{Access: facade.Access, Evaluator: QualityEvaluatorFunc(func(_ context.Context, _ Execution) (QualityResult, error) {
		workspace := manifest.Sources[0].Ref
		return QualityResult{Passed: true, QualityRunID: uuid.NewString(), WorkspaceRevision: &workspace}, nil
	})}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(domain.NodePublish, PublishRunner{Access: facade.Access, Publisher: PublisherFunc(func(_ context.Context, _, _, actorID, _ string, _ WorkflowPublishInput) (PublishResult, error) {
		publishActor = actorID
		return PublishResult{URL: "/published/preview", DeploymentID: uuid.NewString()}, nil
	})}); err != nil {
		t.Fatal(err)
	}
	installCompleteTestExecutionProfileRuntime(t, engine, registry)
	run, err := facade.Start(context.Background(), manifest.ProjectID, editorID, StartRequest{RunID: uuid.NewString(), DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
			t.Fatal(err)
		}
	}
	if err := facade.AuthorizeExecution(context.Background(), manifest.ProjectID, run.ID, "quality", editorID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
			t.Fatal(err)
		}
	}
	waiting, _ := store.GetRun(context.Background(), run.ID)
	if waiting.Nodes["review"].Status != NodeWaitingReview {
		t.Fatalf("review status=%s", waiting.Nodes["review"].Status)
	}
	if err := facade.ResolveReview(context.Background(), manifest.ProjectID, run.ID, "review", adminID, ReviewApprove, ""); err != nil {
		t.Fatal(err)
	}
	authorized, _ := store.GetRun(context.Background(), run.ID)
	grant := authorized.Context.Nodes["publish"].ExecutionActor
	decisionActor := authorized.Context.Nodes["review"].ReviewDecisionActor
	if authorized.Nodes["publish"].Status != NodeReady || grant == nil || grant.ActorID != adminID || grant.Source != ActorSourceReviewApproval || decisionActor == nil || decisionActor.ActorID != adminID {
		t.Fatalf("review actor handoff missing: publish=%s grant=%+v decision=%+v", authorized.Nodes["publish"].Status, grant, decisionActor)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if publishActor != adminID {
		t.Fatalf("review-authorized publish actor=%q", publishActor)
	}
	events, _ := store.ListEvents(context.Background(), run.ID, 0, 100)
	assertActorEvent(t, events, "node.review_approve", "review", adminID)
	assertActorEvent(t, events, "node.execution_authorized", "publish", adminID)
}

func assertActorEvent(t *testing.T, events []Event, eventType, nodeKey, actorID string) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType && event.NodeKey == nodeKey && event.ActorID == actorID {
			return
		}
	}
	t.Fatalf("missing %s event for node=%s actor=%s: %+v", eventType, nodeKey, actorID, events)
}
