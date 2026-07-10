package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

func governedApplicationDefinition(t *testing.T, id string, version int, actorID string, now time.Time) domain.WorkflowDefinition {
	t.Helper()
	schema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	nodes := []domain.NodeDefinition{
		{ID: "source", Name: "Project Brief", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, AllowedKinds: []string{"project_brief"}, MinimumArtifacts: 1}},
		{ID: "workbench", Name: "Workbench", Type: domain.NodeWorkbenchBuild, InputSchema: schema, OutputSchema: schema, WorkbenchBuild: &domain.WorkbenchBuildNodeConfig{BuildManifestSchemaVersion: 1, MaxAttempts: 1, Timeout: time.Minute}},
		{ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "production", RequiredRole: "admin"}},
	}
	definition, err := domain.NewWorkflowDefinitionWithContracts(
		id, version, "Governed application", "3", nodes,
		[]domain.WorkflowEdge{{ID: "source-workbench", From: "source", To: "workbench"}, {ID: "workbench-publish", From: "workbench", To: "publish"}},
		ProjectBriefInputContract(), ApplicationOutputContract(), actorID, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func governedStartManifest(t *testing.T, projectID, actorID, jobType, schema string, purposes ...string) domain.InputManifest {
	t.Helper()
	hash, err := domain.CanonicalHash(map[string]any{"source": "immutable"})
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: hash}
	sources := make([]domain.ManifestSource, 0, len(purposes))
	for index, purpose := range purposes {
		current := ref
		if index > 0 {
			current.ArtifactID, current.RevisionID = uuid.NewString(), uuid.NewString()
		}
		sources = append(sources, domain.ManifestSource{Ref: current, Purpose: purpose})
	}
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, jobType, "", &ref, sources, json.RawMessage(`{}`), schema, actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestCompatibleStartMatchesFullInputAndDesiredOutputContract(t *testing.T) {
	t.Parallel()
	definition := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	cases := []struct {
		name       string
		descriptor StartManifestDescriptor
		desired    string
		compatible bool
	}{
		{name: "direct", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}}, desired: "application", compatible: true},
		{name: "conversation", descriptor: StartManifestDescriptor{JobType: "conversation.workflow_intent", OutputSchemaVersion: "workflow-intent-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}}, desired: "application", compatible: true},
		{name: "cross schema", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-intent-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}}, desired: "application"},
		{name: "missing purpose", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"requirements"}, ArtifactKinds: []string{"project_brief"}}, desired: "application"},
		{name: "cross kind", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"blueprint"}}, desired: "application"},
		{name: "extra kind", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief", "blueprint"}}, desired: "application"},
		{name: "missing kind", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}}, desired: "application"},
		{name: "wrong output", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}}, desired: "document"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := CompatibleStart(definition, test.descriptor, test.desired)
			if test.compatible && err != nil {
				t.Fatalf("compatible descriptor rejected: %v", err)
			}
			if !test.compatible && !errors.Is(err, ErrStartManifestIncompatible) {
				t.Fatalf("incompatible descriptor error = %v", err)
			}
		})
	}
}

func TestEngineStartResolvesExactKindsBeforeWritingRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	definition := governedApplicationDefinition(t, uuid.NewString(), 1, actorID, time.Now().UTC())
	versionID := uuid.NewString()
	if err := store.SaveDefinition(ctx, DefinitionRecord{VersionID: versionID, ProjectID: projectID, Key: "governed-app", Title: "Governed", Published: true, Definition: definition}); err != nil {
		t.Fatal(err)
	}
	manifest := governedStartManifest(t, projectID, actorID, "workflow_start", "workflow-input/v1", "project_brief")
	if err := store.SaveManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	for _, kinds := range [][]string{{}, {"blueprint"}, {"project_brief", "blueprint"}} {
		engine, err := NewEngine(store)
		if err != nil {
			t.Fatal(err)
		}
		engine.StartArtifactKinds = fixedStartArtifactKinds(kinds)
		before := len(store.runs)
		if _, err := engine.Start(ctx, StartRequest{ProjectID: projectID, DefinitionVersionID: versionID, InputManifest: manifest.Ref(), StartedBy: actorID}); !errors.Is(err, ErrStartManifestIncompatible) && !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("kinds %v reached run creation: %v", kinds, err)
		}
		if len(store.runs) != before {
			t.Fatalf("kinds %v persisted a run before exact contract validation", kinds)
		}
	}

	schema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	unsupportedNodes := []domain.NodeDefinition{
		{ID: "source", Name: "Source", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, AllowedKinds: []string{"project_brief"}, MinimumArtifacts: 1}},
		{ID: "unsupported", Name: "Unsupported", Type: domain.NodeAITransform, InputSchema: schema, OutputSchema: schema, AITransform: &domain.AITransformNodeConfig{JobType: "custom_transform", ModelPolicy: "default", OutputSchemaVersion: "artifact/v1", MaxAttempts: 1, Timeout: time.Minute}},
		{ID: "workbench", Name: "Workbench", Type: domain.NodeWorkbenchBuild, InputSchema: schema, OutputSchema: schema, WorkbenchBuild: &domain.WorkbenchBuildNodeConfig{BuildManifestSchemaVersion: 1, MaxAttempts: 1, Timeout: time.Minute}},
		{ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &domain.PublishNodeConfig{Environment: "production", RequiredRole: "admin"}},
	}
	unsupported, err := domain.NewWorkflowDefinitionWithContracts(
		uuid.NewString(), 1, "Unsupported", "3", unsupportedNodes,
		[]domain.WorkflowEdge{{ID: "e1", From: "source", To: "unsupported"}, {ID: "e2", From: "unsupported", To: "workbench"}, {ID: "e3", From: "workbench", To: "publish"}},
		ProjectBriefInputContract(), ApplicationOutputContract(), actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	unsupportedVersionID := uuid.NewString()
	if err := store.SaveDefinition(ctx, DefinitionRecord{VersionID: unsupportedVersionID, ProjectID: projectID, Key: "unsupported-app", Title: "Unsupported", Published: true, Definition: unsupported}); err != nil {
		t.Fatal(err)
	}
	engine, _ := NewEngine(store)
	engine.StartArtifactKinds = fixedStartArtifactKinds{"project_brief"}
	before := len(store.runs)
	if _, err := engine.Start(ctx, StartRequest{ProjectID: projectID, DefinitionVersionID: unsupportedVersionID, InputManifest: manifest.Ref(), StartedBy: actorID}); err == nil {
		t.Fatal("published definition with an unregistered AI capability started")
	}
	if len(store.runs) != before {
		t.Fatal("unsupported published definition persisted a run")
	}
}

func TestCapabilityRegistryRejectsUnregisteredExecutionCapabilities(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	base := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	schema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	insert := func(node domain.NodeDefinition) domain.WorkflowDefinition {
		nodes := append([]domain.NodeDefinition(nil), base.Nodes...)
		nodes = append(nodes[:1], append([]domain.NodeDefinition{node}, nodes[1:]...)...)
		edges := []domain.WorkflowEdge{{ID: "source-extra", From: "source", To: node.ID}, {ID: "extra-workbench", From: node.ID, To: "workbench"}, {ID: "workbench-publish", From: "workbench", To: "publish"}}
		definition, err := domain.NewWorkflowDefinitionWithContracts(base.ID, 2, base.Name, base.SchemaVersion, nodes, edges, *base.InputContract, *base.OutputContract, base.CreatedBy, base.CreatedAt.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return definition
	}
	tests := []domain.NodeDefinition{
		{ID: "bad-ai-job", Name: "Bad AI", Type: domain.NodeAITransform, InputSchema: schema, OutputSchema: schema, AITransform: &domain.AITransformNodeConfig{JobType: "custom_transform", ModelPolicy: "project-default", OutputSchemaVersion: "artifact/v1", MaxAttempts: 1, Timeout: time.Minute}},
		{ID: "bad-ai-policy", Name: "Bad policy", Type: domain.NodeAITransform, InputSchema: schema, OutputSchema: schema, AITransform: &domain.AITransformNodeConfig{JobType: "derive_requirements", ModelPolicy: "unregistered", OutputSchemaVersion: "requirements-proposal/v1", MaxAttempts: 1, Timeout: time.Minute}},
		{ID: "bad-compiler", Name: "Bad compiler", Type: domain.NodeManifestCompiler, InputSchema: schema, OutputSchema: schema, ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "v1"}},
	}
	for _, node := range tests {
		if err := capabilities.ValidateDefinition(insert(node)); err == nil {
			t.Fatalf("unregistered capability on %s was accepted", node.ID)
		}
	}
}

func TestCompatibleDefinitionVersionsUsesTrustedManifestAndHighestPublishedVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID, actorID, definitionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	for version := 1; version <= 3; version++ {
		definition := governedApplicationDefinition(t, definitionID, version, actorID, time.Now().UTC().Add(time.Duration(version)*time.Minute))
		if err := store.SaveDefinition(ctx, DefinitionRecord{
			VersionID: uuid.NewString(), ProjectID: projectID, Key: "governed-app", Title: "Governed",
			Published: version < 3, Definition: definition,
		}); err != nil {
			t.Fatal(err)
		}
	}
	manifest := governedStartManifest(t, projectID, actorID, "conversation.workflow_intent", "workflow-intent-input/v1", "project_brief")
	if err := store.SaveManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	engine, _ := NewEngine(store)
	engine.StartArtifactKinds = fixedStartArtifactKinds{"project_brief"}
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{}}
	versions, err := facade.CompatibleDefinitionVersions(ctx, projectID, actorID, manifest.Ref(), domain.WorkflowOutputApplication)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Definition.ID != definitionID || versions[0].Definition.Version != 2 {
		t.Fatalf("compatible versions = %+v", versions)
	}
	if versions, err := facade.CompatibleDefinitionVersions(ctx, projectID, actorID, manifest.Ref(), "document"); err != nil || len(versions) != 0 {
		t.Fatalf("wrong desired output discovery = %+v, %v", versions, err)
	}
}
