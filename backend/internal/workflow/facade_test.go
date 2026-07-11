package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type fixedWorkflowAccess struct{ role core.Role }

func (a fixedWorkflowAccess) Authorize(context.Context, string, string, core.Action) (core.Role, error) {
	return a.role, nil
}

type fixedStartArtifactKinds []string

func (k fixedStartArtifactKinds) ResolveStartArtifactKinds(context.Context, domain.InputManifest) ([]string, error) {
	return append([]string(nil), k...), nil
}

func authoredWorkflowFixture(t *testing.T) ([]domain.NodeDefinition, []domain.WorkflowEdge) {
	t.Helper()
	definition, err := MinimumLoopDefinition(uuid.NewString(), uuid.NewString(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return definition.Nodes, definition.Edges
}

func TestWorkflowDefinitionAuthoringVersionsAndPublishesImmutableGraphs(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore(nil)
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	engine.StartArtifactKinds = startArtifactKindResolverFunc(func(context.Context, domain.InputManifest) ([]string, error) {
		return []string{"project_brief"}, nil
	})
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{role: core.RoleOwner}}
	projectID, ownerID := uuid.NewString(), uuid.NewString()
	nodes, edges := authoredWorkflowFixture(t)
	created, err := facade.CreateDefinition(context.Background(), projectID, ownerID, CreateDefinitionInput{
		Key: "custom-delivery", Title: "Custom delivery", Nodes: nodes, Edges: edges,
		InputContract: ProjectBriefInputContract(), OutputContract: ApplicationOutputContract(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Published || created.Definition.Version != 1 || created.Definition.Hash == "" {
		t.Fatalf("unexpected draft definition: %+v", created)
	}
	published, err := facade.PublishDefinitionVersion(context.Background(), projectID, created.Definition.ID, created.VersionID, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if !published.Published || published.Definition.Hash != created.Definition.Hash {
		t.Fatalf("publish mutated immutable graph: %+v", published)
	}
	second, err := facade.CreateDefinitionVersion(context.Background(), projectID, created.Definition.ID, ownerID, CreateDefinitionVersionInput{
		Name: "Custom delivery v2", Nodes: nodes, Edges: edges,
		InputContract: ProjectBriefInputContract(), OutputContract: ApplicationOutputContract(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Published || second.Definition.Version != 2 || second.Definition.Hash == created.Definition.Hash {
		t.Fatalf("unexpected second version: %+v", second)
	}
	versions, err := facade.ListDefinitionVersions(context.Background(), projectID, created.Definition.ID, ownerID)
	if err != nil || len(versions) != 2 || !versions[0].Published || versions[1].Published {
		t.Fatalf("version history = %+v, error = %v", versions, err)
	}
}

func TestWorkflowDefinitionAuthoringRejectsUnknownRoles(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore(nil)
	engine, _ := NewEngine(store)
	engine.StartArtifactKinds = startArtifactKindResolverFunc(func(context.Context, domain.InputManifest) ([]string, error) {
		return []string{"project_brief"}, nil
	})
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{role: core.RoleOwner}}
	schema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	_, err := facade.CreateDefinition(context.Background(), uuid.NewString(), uuid.NewString(), CreateDefinitionInput{
		Key: "unsafe-role", Title: "Unsafe role",
		Nodes: []domain.NodeDefinition{
			{ID: "input", Name: "Input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}},
			{ID: "edit", Name: "Edit", Type: domain.NodeHumanEdit, InputSchema: schema, OutputSchema: schema, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, RequiredRole: "superuser"}},
		},
		Edges: []domain.WorkflowEdge{{ID: "edge", From: "input", To: "edit"}},
	})
	if err == nil {
		t.Fatal("unknown workflow role was accepted")
	}
}

func TestListingAndDefaultStartAreSideEffectFreeUntilExplicitProvisioning(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore(nil)
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	engine.StartArtifactKinds = fixedStartArtifactKinds{"project_brief"}
	engine.StartArtifactKinds = startArtifactKindResolverFunc(func(context.Context, domain.InputManifest) ([]string, error) {
		return []string{"project_brief"}, nil
	})
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{role: core.RoleOwner}}
	projectID, ownerID := uuid.NewString(), uuid.NewString()
	first, err := facade.ListDefinitions(context.Background(), projectID, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := facade.ListDefinitions(context.Background(), projectID, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 || len(second) != 0 {
		t.Fatalf("GET-style listing mutated workflow state: first=%+v second=%+v", first, second)
	}
	manifest := testManifestForEngine(t, projectID, ownerID, time.Now().UTC())
	if err := store.SaveManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := facade.Start(context.Background(), projectID, ownerID, StartRequest{InputManifest: manifest.Ref()}); err == nil {
		t.Fatal("default start implicitly installed the minimum loop")
	}
	definitionID := uuid.NewSHA1(uuid.MustParse(projectID), []byte("worksflow:minimum-loop:definition")).String()
	versionID, err := minimumLoopVersionID(projectID, MinimumLoopCurrentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SeedMinimumLoop(context.Background(), store, MinimumLoopSeed{
		DefinitionID: definitionID, VersionID: versionID, ProjectID: projectID,
		InstallerUserID: ownerID, Published: true,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := facade.Start(context.Background(), projectID, ownerID, StartRequest{InputManifest: manifest.Ref()}); err != nil {
		t.Fatalf("explicitly provisioned minimum loop did not start: %v", err)
	}
	installed, err := facade.ListDefinitions(context.Background(), projectID, ownerID)
	if err != nil || len(installed) != 1 || installed[0].Key != MinimumLoopKey || !installed[0].Published {
		t.Fatalf("explicit provisioner did not install the template once: %+v, error=%v", installed, err)
	}
}

func TestListRunsUsesStableOpaqueCursorAndProjectScope(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore(nil)
	engine, _ := NewEngine(store)
	engine.StartArtifactKinds = startArtifactKindResolverFunc(func(context.Context, domain.InputManifest) ([]string, error) {
		return []string{"project_brief"}, nil
	})
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{role: core.RoleOwner}}
	projectID, actorID := uuid.NewString(), uuid.NewString()
	base := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		run := &RunRecord{
			ID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: uuid.NewString(),
			Status: RunRunning, StartedBy: actorID, CreatedAt: base.Add(time.Duration(index) * time.Minute),
			UpdatedAt: base.Add(time.Duration(index) * time.Minute),
		}
		store.runs[run.ID] = run
	}
	foreignID := uuid.NewString()
	store.runs[foreignID] = &RunRecord{ID: foreignID, ProjectID: uuid.NewString(), Status: RunRunning, CreatedAt: base.Add(time.Hour)}
	first, err := facade.ListRuns(context.Background(), projectID, actorID, RunListOptions{Limit: 2, Status: RunRunning})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, error = %v", first, err)
	}
	second, err := facade.ListRuns(context.Background(), projectID, actorID, RunListOptions{Limit: 2, Status: RunRunning, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %+v, error = %v", second, err)
	}
	if _, err := facade.ListRuns(context.Background(), projectID, actorID, RunListOptions{Cursor: "not-a-cursor"}); err == nil {
		t.Fatal("invalid cursor was accepted")
	}
}

func TestDefinitionListKeepsLatestDraftVisibleWhileDefaultStartUsesPublishedVersion(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore(nil)
	engine, _ := NewEngine(store)
	engine.StartArtifactKinds = fixedStartArtifactKinds{"project_brief"}
	engine.StartArtifactKinds = startArtifactKindResolverFunc(func(context.Context, domain.InputManifest) ([]string, error) {
		return []string{"project_brief"}, nil
	})
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{role: core.RoleOwner}}
	projectID, actorID := uuid.NewString(), uuid.NewString()
	if _, err := SeedMinimumLoop(context.Background(), store, MinimumLoopSeed{
		DefinitionID: uuid.NewString(), VersionID: uuid.NewString(), ProjectID: projectID,
		InstallerUserID: actorID, Published: true,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	definitions, err := facade.ListDefinitions(context.Background(), projectID, actorID)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("seed definitions = %+v, error = %v", definitions, err)
	}
	published := definitions[0]
	draft, err := facade.CreateDefinitionVersion(context.Background(), projectID, published.Definition.ID, actorID, CreateDefinitionVersionInput{
		Name: "Minimum loop draft", SchemaVersion: published.Definition.SchemaVersion,
		Nodes: published.Definition.Nodes, Edges: published.Definition.Edges,
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := facade.ListDefinitions(context.Background(), projectID, actorID)
	if err != nil || len(listed) != 1 || listed[0].VersionID != draft.VersionID || listed[0].Published {
		t.Fatalf("latest draft is not visible: %+v, error = %v", listed, err)
	}
	manifest := testManifestForEngine(t, projectID, actorID, time.Now().UTC())
	if err := store.SaveManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	run, err := facade.Start(context.Background(), projectID, actorID, StartRequest{InputManifest: manifest.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	if run.DefinitionVersionID != published.VersionID {
		t.Fatalf("default start used %s, want published %s", run.DefinitionVersionID, published.VersionID)
	}
}
