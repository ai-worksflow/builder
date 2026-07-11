package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

func rebuildProfileDefinition(t *testing.T, base domain.WorkflowDefinition, nodes []domain.NodeDefinition, edges []domain.WorkflowEdge) domain.WorkflowDefinition {
	t.Helper()
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		base.ID, base.Version, base.Name, base.SchemaVersion, nodes, edges,
		*base.InputContract, *base.OutputContract, CurrentWorkflowExecutionProfileRef(), base.CreatedBy, base.CreatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func profileConditionDefinition(t *testing.T, base domain.WorkflowDefinition, count int, expression string) domain.WorkflowDefinition {
	t.Helper()
	edge := base.Edges[0]
	target, _ := base.FindNode(edge.To)
	nodes := append([]domain.NodeDefinition(nil), base.Nodes...)
	edges := append([]domain.WorkflowEdge(nil), base.Edges[1:]...)
	previous := edge.From
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("profile-condition-%02d", index)
		branchExpression := "true"
		if index == 0 && expression != "" {
			branchExpression = expression
		}
		nodes = append(nodes, domain.NodeDefinition{
			ID: id, Name: id, Type: domain.NodeCondition, InputSchema: target.InputSchema,
			OutputPorts: map[string]domain.PortDefinition{"yes": {Schema: target.InputSchema}, "no": {Schema: target.InputSchema}},
			Condition:   &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{{Name: "yes", Expression: branchExpression}, {Name: "no", Default: true}}},
		})
		if index == 0 {
			edges = append(edges, domain.WorkflowEdge{ID: "profile-condition-entry", From: previous, FromPort: edge.FromPort, To: id})
		} else {
			edges = append(edges,
				domain.WorkflowEdge{ID: id + "-yes", From: previous, FromPort: "yes", To: id},
				domain.WorkflowEdge{ID: id + "-no", From: previous, FromPort: "no", To: id},
			)
		}
		previous = id
	}
	edges = append(edges,
		domain.WorkflowEdge{ID: "profile-condition-exit-yes", From: previous, FromPort: "yes", To: edge.To, ToPort: edge.ToPort},
		domain.WorkflowEdge{ID: "profile-condition-exit-no", From: previous, FromPort: "no", To: edge.To, ToPort: edge.ToPort},
	)
	return rebuildProfileDefinition(t, base, nodes, edges)
}

func unsafeInstallProfileDefinition(store *MemoryStore, record DefinitionRecord) {
	store.mu.Lock()
	defer store.mu.Unlock()
	copyRecord := cloneDefinitionRecord(record)
	store.definitions[record.Definition.ID] = map[int]DefinitionRecord{record.Definition.Version: copyRecord}
	store.definitionVersions[record.VersionID] = copyRecord
}

func assertProfileDefinitionRejectedAtEveryStage(t *testing.T, definition domain.WorkflowDefinition) {
	t.Helper()
	ctx := context.Background()
	projectID, actorID, versionID := uuid.NewString(), definition.CreatedBy, uuid.NewString()
	record := DefinitionRecord{VersionID: versionID, ProjectID: projectID, Key: "profile-invalid", Title: "Invalid", Published: true, ExecutionProfile: CurrentWorkflowExecutionProfileRef(), Definition: definition}
	expected := ValidateDefinitionForExecutionProfile(definition, record.ExecutionProfile)
	if expected == nil {
		t.Fatal("invalid profile fixture passed the canonical validator")
	}

	store := NewMemoryStore(nil)
	if err := store.SaveDefinition(ctx, record); err == nil || err.Error() != expected.Error() {
		t.Fatalf("SaveDefinition rejection drifted: got=%v want=%v", err, expected)
	}

	stages := []struct {
		name string
		run  func(*MemoryStore, *Engine, Facade) error
	}{
		{name: "create", run: func(_ *MemoryStore, _ *Engine, facade Facade) error {
			_, err := facade.CreateDefinition(ctx, projectID, actorID, CreateDefinitionInput{
				Key: "profile-invalid-create", Title: "Invalid create", Name: definition.Name, SchemaVersion: definition.SchemaVersion,
				Nodes: definition.Nodes, Edges: definition.Edges, InputContract: *definition.InputContract, OutputContract: *definition.OutputContract,
			})
			return err
		}},
		{name: "publish", run: func(store *MemoryStore, _ *Engine, facade Facade) error {
			_, err := facade.PublishDefinitionVersion(ctx, projectID, definition.ID, versionID, actorID)
			return err
		}},
		{name: "discover", run: func(store *MemoryStore, engine *Engine, facade Facade) error {
			manifest := governedStartManifest(t, projectID, actorID, "workflow_start", "workflow-input/v1", "project_brief")
			if err := store.SaveManifest(ctx, manifest); err != nil {
				t.Fatal(err)
			}
			engine.StartArtifactKinds = &fixedStartMetadataResolver{metadata: StartArtifactMetadata{Kinds: []string{"project_brief"}, Count: 1}}
			_, err := facade.DiscoverCompatibleDefinitionVersions(ctx, projectID, actorID, DefinitionDiscoveryRequest{InputManifest: manifest.Ref(), DesiredOutputCapability: domain.WorkflowOutputApplication})
			return err
		}},
		{name: "start", run: func(store *MemoryStore, engine *Engine, _ Facade) error {
			_, err := engine.Start(ctx, StartRequest{RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: versionID, InputManifest: domain.ManifestRef{ID: uuid.NewString(), Hash: strings.Repeat("a", 64)}, StartedBy: actorID})
			return err
		}},
		{name: "runtime-load", run: func(store *MemoryStore, engine *Engine, _ Facade) error {
			now := time.Now().UTC()
			runID := uuid.NewString()
			store.mu.Lock()
			store.runs[runID] = &RunRecord{ID: runID, ProjectID: projectID, DefinitionVersionID: versionID, Definition: definition.Ref(), ExecutionProfile: CurrentWorkflowExecutionProfileRef(), Status: RunRunning, Scope: []byte(`{}`), Context: NewRunContext(), StartedBy: actorID, CreatedAt: now, UpdatedAt: now, Nodes: map[string]*NodeRecord{}}
			store.mu.Unlock()
			_, _, err := engine.loadRun(ctx, runID)
			return err
		}},
	}
	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			stageStore := NewMemoryStore(nil)
			unsafeInstallProfileDefinition(stageStore, record)
			engine, err := NewEngine(stageStore)
			if err != nil {
				t.Fatal(err)
			}
			installCompleteTestExecutionProfileRuntime(t, engine, nil)
			facade := Facade{Engine: engine, Store: stageStore, Access: actorTestAccess{roles: map[string]core.Role{actorID: core.RoleOwner}}}
			if err := stage.run(stageStore, engine, facade); err == nil || err.Error() != expected.Error() {
				t.Fatalf("%s rejection drifted: got=%v want=%v", stage.name, err, expected)
			}
		})
	}
}

func TestCurrentProfileContractlessDefinitionIsRejectedAtPersistenceControlAndRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	actorID, projectID := uuid.NewString(), uuid.NewString()
	legacy := simpleDefinition(t, uuid.NewString(), actorID, time.Now().UTC())
	definition, err := legacy.WithExecutionProfile(CurrentWorkflowExecutionProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	expected := ValidateDefinitionForExecutionProfile(definition, CurrentWorkflowExecutionProfileRef())
	if expected == nil || !strings.Contains(expected.Error(), "inputContract and outputContract") {
		t.Fatalf("contractless current definition did not fail the canonical validator: %v", expected)
	}
	record := DefinitionRecord{
		VersionID: uuid.NewString(), ProjectID: projectID, Key: "contractless-current", Title: "Contractless current",
		Published: true, ExecutionProfile: CurrentWorkflowExecutionProfileRef(), Definition: definition,
	}
	if err := NewMemoryStore(nil).SaveDefinition(ctx, record); err == nil || err.Error() != expected.Error() {
		t.Fatalf("Published=true persistence rejection drifted: got=%v want=%v", err, expected)
	}

	manifest := governedStartManifest(t, projectID, actorID, "workflow_start", "workflow-input/v1", "project_brief")
	for _, stage := range []struct {
		name string
		run  func(*MemoryStore, *Engine, Facade) error
	}{
		{name: "publish", run: func(store *MemoryStore, _ *Engine, _ Facade) error {
			_, err := store.PublishDefinitionVersion(ctx, projectID, definition.ID, record.VersionID, actorID)
			return err
		}},
		{name: "discover", run: func(store *MemoryStore, engine *Engine, facade Facade) error {
			if err := store.SaveManifest(ctx, manifest); err != nil {
				t.Fatal(err)
			}
			engine.StartArtifactKinds = &fixedStartMetadataResolver{metadata: StartArtifactMetadata{Kinds: []string{"project_brief"}, Count: 1}}
			_, err := facade.DiscoverCompatibleDefinitionVersions(ctx, projectID, actorID, DefinitionDiscoveryRequest{
				InputManifest: manifest.Ref(), DesiredOutputCapability: domain.WorkflowOutputApplication,
			})
			return err
		}},
		{name: "start", run: func(store *MemoryStore, _ *Engine, facade Facade) error {
			if err := store.SaveManifest(ctx, manifest); err != nil {
				t.Fatal(err)
			}
			_, err := facade.Start(ctx, projectID, actorID, StartRequest{
				RunID: uuid.NewString(), DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(),
			})
			return err
		}},
		{name: "runtime-load", run: func(store *MemoryStore, engine *Engine, _ Facade) error {
			now := time.Now().UTC()
			runID := uuid.NewString()
			store.mu.Lock()
			store.runs[runID] = &RunRecord{
				ID: runID, ProjectID: projectID, DefinitionVersionID: record.VersionID,
				Definition: definition.Ref(), ExecutionProfile: CurrentWorkflowExecutionProfileRef(),
				Status: RunRunning, Scope: json.RawMessage(`{}`), Context: NewRunContext(), StartedBy: actorID,
				CreatedAt: now, UpdatedAt: now, Nodes: map[string]*NodeRecord{},
			}
			store.mu.Unlock()
			_, _, err := engine.loadRun(ctx, runID)
			return err
		}},
	} {
		t.Run(stage.name, func(t *testing.T) {
			store := NewMemoryStore(nil)
			unsafeInstallProfileDefinition(store, record)
			engine, err := NewEngine(store)
			if err != nil {
				t.Fatal(err)
			}
			installCompleteTestExecutionProfileRuntime(t, engine, nil)
			facade := Facade{Engine: engine, Store: store, Access: actorTestAccess{roles: map[string]core.Role{actorID: core.RoleOwner}}}
			if err := stage.run(store, engine, facade); err == nil || err.Error() != expected.Error() {
				t.Fatalf("%s rejection drifted: got=%v want=%v", stage.name, err, expected)
			}
		})
	}
}

func TestCurrentProfileValidatorIsIdenticalAcrossPersistenceControlAndRuntime(t *testing.T) {
	base := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())

	roleNodes := append([]domain.NodeDefinition(nil), base.Nodes...)
	for index := range roleNodes {
		if roleNodes[index].HumanEdit != nil {
			config := *roleNodes[index].HumanEdit
			config.RequiredRole = "root"
			roleNodes[index].HumanEdit = &config
			break
		}
	}
	invalidRole := rebuildProfileDefinition(t, base, roleNodes, base.Edges)
	invalidDSL := profileConditionDefinition(t, base, 1, `{"shell":"rm"}`)
	oversizeCondition := profileConditionDefinition(t, base, 1, `"`+strings.Repeat("x", CurrentWorkflowExecutionProfileDescriptor().Capabilities.AnalysisLimits.MaximumConditionExpression)+`"`)
	semanticBomb := profileConditionDefinition(t, base, 9, "")

	for name, definition := range map[string]domain.WorkflowDefinition{
		"role": invalidRole, "condition-dsl": invalidDSL, "condition-8kb": oversizeCondition, "semantic-budget": semanticBomb,
	} {
		t.Run(name, func(t *testing.T) { assertProfileDefinitionRejectedAtEveryStage(t, definition) })
	}

	if err := ValidateDefinitionForExecutionProfile(base, CurrentWorkflowExecutionProfileRef()); err != nil {
		t.Fatalf("valid current profile definition was rejected: %v", err)
	}
	legacy := simpleDefinition(t, uuid.NewString(), uuid.NewString(), time.Now().UTC())
	if err := ValidateDefinitionForExecutionProfile(legacy, LegacyWorkflowExecutionProfileRef()); err != nil {
		t.Fatalf("frozen legacy replay validator rejected pre-pin definition: %v", err)
	}
}

func TestGORMStoreProfileValidationCannotBeBypassedByPublishedFlagPostgres(t *testing.T) {
	database, cleanup := multiBundleCompletionPostgresDatabase(t)
	defer cleanup()
	ctx := context.Background()
	actorID, projectID := uuid.New(), uuid.New()
	if err := database.Exec(`INSERT INTO users (id,email,display_name,password_hash) VALUES (?,?,?,?)`, actorID, actorID.String()+"@profile-validation.test", "Owner", "unused").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO projects (id,name,created_by) VALUES (?,?,?)`, projectID, "Profile validation", actorID).Error; err != nil {
		t.Fatal(err)
	}
	base := governedApplicationDefinition(t, uuid.NewString(), 1, actorID.String(), time.Now().UTC())
	nodes := append([]domain.NodeDefinition(nil), base.Nodes...)
	for index := range nodes {
		if nodes[index].Publish != nil {
			config := *nodes[index].Publish
			config.RequiredRole = "superuser"
			nodes[index].Publish = &config
			break
		}
	}
	invalid := rebuildProfileDefinition(t, base, nodes, base.Edges)
	store, err := NewGORMStore(database, InlineContentStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID := uuid.NewString()
	record := DefinitionRecord{VersionID: versionID, ProjectID: projectID.String(), Key: "published-bypass", Title: "Published bypass", Published: true, ExecutionProfile: CurrentWorkflowExecutionProfileRef(), Definition: invalid}
	if err := store.SaveDefinition(ctx, record); err == nil {
		t.Fatal("GORM SaveDefinition accepted invalid current profile content through Published=true")
	}
	var count int64
	if err := database.Model(&definitionRow{}).Where("id = ?", invalid.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rejected published definition left persistent state: count=%d err=%v", count, err)
	}
	legacyContractless := simpleDefinition(t, uuid.NewString(), actorID.String(), time.Now().UTC())
	contractless, err := legacyContractless.WithExecutionProfile(CurrentWorkflowExecutionProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	contractlessRecord := DefinitionRecord{
		VersionID: uuid.NewString(), ProjectID: projectID.String(), Key: "published-contractless-bypass", Title: "Published contractless bypass",
		Published: true, ExecutionProfile: CurrentWorkflowExecutionProfileRef(), Definition: contractless,
	}
	if err := store.SaveDefinition(ctx, contractlessRecord); err == nil || !strings.Contains(err.Error(), "inputContract and outputContract") {
		t.Fatalf("GORM SaveDefinition accepted contractless current content through Published=true: %v", err)
	}
	count = 0
	if err := database.Model(&definitionRow{}).Where("id = ?", contractless.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rejected contractless definition left persistent state: count=%d err=%v", count, err)
	}

	definitionID, rawVersionID := uuid.MustParse(invalid.ID), uuid.New()
	if err := database.Exec(`INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES (?,?,?,?,?)`, definitionID, projectID, "raw-invalid", "Raw invalid", actorID).Error; err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	profile := CurrentWorkflowExecutionProfileRef()
	if err := database.Exec(`INSERT INTO workflow_definition_versions (id,definition_id,version,schema_version,content,content_hash,execution_profile_version,execution_profile_hash,validation_report,published,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?::jsonb,false,?,?)`, rawVersionID, definitionID, 1, 3, content, invalid.Hash, profile.Version, profile.Hash, `{"valid":true}`, actorID, invalid.CreatedAt).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishDefinitionVersion(ctx, projectID.String(), definitionID.String(), rawVersionID.String(), actorID.String()); err == nil {
		t.Fatal("direct GORM publication bypassed exact execution-profile validation")
	}
	var published bool
	if err := database.Raw(`SELECT published FROM workflow_definition_versions WHERE id = ?`, rawVersionID).Scan(&published).Error; err != nil || published {
		t.Fatalf("rejected invalid definition was published: published=%t err=%v", published, err)
	}
}
