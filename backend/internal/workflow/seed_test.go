package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestMinimumLoopSeedIsPublishedOnlyByInstallerAndValid(t *testing.T) {
	store := NewMemoryStore(nil)
	seed := MinimumLoopSeed{DefinitionID: uuid.NewString(), VersionID: uuid.NewString(), ProjectID: uuid.NewString(), InstallerUserID: uuid.NewString(), Published: true}
	record, err := SeedMinimumLoop(context.Background(), store, seed, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Definition.Validate(); err != nil {
		t.Fatal(err)
	}
	if record.Definition.CreatedBy != seed.InstallerUserID {
		t.Fatal("seed must preserve installer user")
	}
	if record.Definition.Version != MinimumLoopCurrentVersion || record.Definition.SchemaVersion != "5" {
		t.Fatalf("current seed version = %d schema = %s", record.Definition.Version, record.Definition.SchemaVersion)
	}
	if len(record.Definition.Nodes) != 22 || len(record.Definition.Edges) != 21 {
		t.Fatalf("current topology has %d nodes/%d edges, want 22/21", len(record.Definition.Nodes), len(record.Definition.Edges))
	}
	required := map[domain.WorkflowNodeType]bool{domain.NodeArtifactInput: false, domain.NodeAITransform: false, domain.NodeHumanEdit: false, domain.NodeReviewGate: false, domain.NodeFanOut: false, domain.NodeMerge: false, domain.NodeManifestCompiler: false, domain.NodeWorkbenchBuild: false, domain.NodeQualityGate: false, domain.NodePublish: false}
	wantKinds := map[string]string{
		"project-brief-edit": "project_brief", "requirements-edit": "product_requirements",
		"blueprint-edit": "blueprint", "page-spec-edit": "page_spec", "prototype-edit": "prototype",
	}
	wantJobs := map[string]string{
		"project-brief-ai": "refine_project_brief", "requirements-ai": "derive_requirements",
		"blueprint-ai": "decompose_pages", "page-spec-ai": "generate_page_spec",
		"prototype-ai": "generate_prototype",
	}
	for _, node := range record.Definition.Nodes {
		if _, exists := required[node.Type]; exists {
			required[node.Type] = true
		}
		if node.ID == "pages" {
			if node.FanOut == nil || node.FanOut.ItemKind != "blueprint_page" || node.FanOut.ItemsPath != "/blueprintPages" || node.FanOut.SliceKeyPath != "/key" || node.FanOut.MaxItems != domain.MaximumWorkflowFanOutItems {
				t.Fatalf("minimum loop page fan-out is not an exact Blueprint-page mode: %+v", node.FanOut)
			}
		}
		if wantKind, exists := wantKinds[node.ID]; exists && (node.HumanEdit == nil || node.HumanEdit.ArtifactKind != wantKind) {
			t.Fatalf("node %s exact artifact kind = %+v, want %s", node.ID, node.HumanEdit, wantKind)
		}
		if wantJob, exists := wantJobs[node.ID]; exists && (node.AITransform == nil || node.AITransform.JobType != wantJob) {
			t.Fatalf("node %s AI job = %+v, want %s", node.ID, node.AITransform, wantJob)
		}
	}
	for nodeType, present := range required {
		if !present {
			t.Fatalf("minimum loop missing %s", nodeType)
		}
	}
	wantEdges := [][2]string{
		{"source", "project-brief-ai"}, {"project-brief-ai", "project-brief-edit"},
		{"project-brief-edit", "project-brief-review"}, {"project-brief-review", "requirements-ai"},
		{"requirements-ai", "requirements-edit"}, {"requirements-edit", "requirements-review"},
		{"requirements-review", "blueprint-ai"}, {"blueprint-ai", "blueprint-edit"},
		{"blueprint-edit", "blueprint-review"}, {"blueprint-review", "pages"},
		{"pages", "page-spec-ai"}, {"page-spec-ai", "page-spec-edit"},
		{"page-spec-edit", "page-spec-review"}, {"page-spec-review", "prototype-ai"},
		{"prototype-ai", "prototype-edit"}, {"prototype-edit", "prototype-review"},
		{"prototype-review", "pages-merged"}, {"pages-merged", "compile-manifest"},
		{"compile-manifest", "workbench"}, {"workbench", "quality"}, {"quality", "publish"},
	}
	for index, want := range wantEdges {
		got := record.Definition.Edges[index]
		if got.From != want[0] || got.To != want[1] {
			t.Fatalf("edge %d = %s -> %s, want %s -> %s", index+1, got.From, got.To, want[0], want[1])
		}
	}
	encoded, err := MinimumLoopDefinitionJSON(seed.DefinitionID, seed.InstallerUserID, time.Now())
	if err != nil || len(encoded) == 0 {
		t.Fatalf("expected canonical seed JSON: %v", err)
	}
	versions, err := store.ListDefinitionVersions(context.Background(), seed.DefinitionID)
	if err != nil || len(versions) != 5 {
		t.Fatalf("seed versions = %+v, error = %v", versions, err)
	}
	if versions[0].Definition.Version != 1 || versions[0].Published ||
		versions[1].Definition.Version != 2 || versions[1].Published ||
		versions[2].Definition.Version != 3 || versions[2].Published ||
		versions[3].Definition.Version != 4 || versions[3].Published || versions[3].ExecutionProfile != WorkflowExecutionProfileV1Ref() || versions[3].Definition.ExecutionProfile != WorkflowExecutionProfileV1Ref() ||
		versions[4].VersionID != seed.VersionID || !versions[4].Published || versions[4].ExecutionProfile != CurrentWorkflowExecutionProfileRef() {
		t.Fatalf("seed version publication is not v1/v2/v3/v4 history -> v5 published with frozen profiles: %+v", versions)
	}
	second, err := SeedMinimumLoop(context.Background(), store, seed, time.Now())
	if err != nil || second.VersionID != record.VersionID {
		t.Fatalf("idempotent seed = %+v, error = %v", second, err)
	}
}

func TestBlueprintSelectionFlowSeedIsExecutableAndIdempotent(t *testing.T) {
	store := NewMemoryStore(nil)
	seed := BlueprintSelectionFlowSeed{
		DefinitionID: uuid.NewString(), VersionID: uuid.NewString(), ProjectID: uuid.NewString(),
		InstallerUserID: uuid.NewString(), Published: true,
	}
	record, err := SeedBlueprintSelectionFlow(context.Background(), store, seed, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Definition.Validate(); err != nil {
		t.Fatal(err)
	}
	if record.Key != BlueprintSelectionFlowKey || !record.Published || len(record.Definition.Nodes) != 8 || len(record.Definition.Edges) != 7 {
		t.Fatalf("Blueprint selection seed = %#v", record)
	}
	pages, ok := record.Definition.FindNode("pages")
	if !ok || pages.FanOut == nil || pages.FanOut.ItemKind != "blueprint_selection_page" {
		t.Fatalf("selection flow page fan-out = %#v", pages)
	}
	transform, ok := record.Definition.FindNode("page-ready")
	if !ok || transform.Transform == nil || transform.Transform.Transform != "selection_passthrough" {
		t.Fatalf("selection flow passthrough = %#v", transform)
	}
	second, err := SeedBlueprintSelectionFlow(context.Background(), store, seed, time.Now())
	if err != nil || second.VersionID != record.VersionID {
		t.Fatalf("idempotent selection seed = %#v, err=%v", second, err)
	}
	versions, err := store.ListDefinitionVersions(context.Background(), seed.DefinitionID)
	if err != nil || len(versions) != 4 {
		t.Fatalf("selection seed versions = %+v, err=%v", versions, err)
	}
	if versions[2].Definition.Version != 3 || versions[2].ExecutionProfile != WorkflowExecutionProfileV1Ref() || versions[2].Definition.ExecutionProfile != WorkflowExecutionProfileV1Ref() || versions[2].Published ||
		versions[3].Definition.Version != BlueprintSelectionFlowVersion || versions[3].ExecutionProfile != CurrentWorkflowExecutionProfileRef() || !versions[3].Published {
		t.Fatalf("selection v3/v4 execution profiles drifted: %+v", versions)
	}
}

func TestSeedMinimumLoopUpgradesExistingProjectFromV1(t *testing.T) {
	store := NewMemoryStore(nil)
	projectID, definitionID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	legacyVersionID := uuid.NewString()
	legacy, err := minimumLoopDefinitionV1(definitionID, actorID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDefinition(context.Background(), DefinitionRecord{
		VersionID: legacyVersionID, ProjectID: projectID, Key: MinimumLoopKey,
		Title: minimumLoopTitle, Description: minimumLoopDescription,
		Published: true, Definition: legacy,
	}); err != nil {
		t.Fatal(err)
	}
	v2ID, err := minimumLoopVersionID(projectID, MinimumLoopCurrentVersion)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := SeedMinimumLoop(context.Background(), store, MinimumLoopSeed{
		DefinitionID: definitionID, VersionID: v2ID, ProjectID: projectID,
		InstallerUserID: actorID, Published: true,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.ListDefinitionVersions(context.Background(), definitionID)
	if err != nil || len(versions) != 5 {
		t.Fatalf("upgraded versions = %+v, error = %v", versions, err)
	}
	if versions[0].VersionID != legacyVersionID || versions[0].Published ||
		versions[1].Definition.Version != 2 || versions[1].Published ||
		versions[2].Definition.Version != 3 || versions[2].Published ||
		versions[3].Definition.Version != 4 || versions[3].Published || versions[3].ExecutionProfile != WorkflowExecutionProfileV1Ref() ||
		upgraded.Definition.Version != MinimumLoopCurrentVersion || upgraded.VersionID != v2ID || !upgraded.Published {
		t.Fatalf("existing v1 history was not preserved while publishing current version: %+v / %+v", versions, upgraded)
	}
	if versions[4].Definition.SchemaVersion != "5" || len(versions[4].Definition.Nodes) != 22 || versions[4].ExecutionProfile != CurrentWorkflowExecutionProfileRef() {
		t.Fatalf("existing project did not receive current topology: %+v", versions[4].Definition)
	}
}
