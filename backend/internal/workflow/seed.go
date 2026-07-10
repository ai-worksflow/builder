package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

const MinimumLoopKey = "minimum-product-loop"

type MinimumLoopSeed struct {
	DefinitionID    string
	VersionID       string
	ProjectID       string
	InstallerUserID string
	Published       bool
}

// SeedMinimumLoop intentionally requires the installing project owner. The
// current schema has NOT NULL user FKs, so no fake/system UUID is manufactured.
func SeedMinimumLoop(ctx context.Context, store Store, seed MinimumLoopSeed, now time.Time) (DefinitionRecord, error) {
	if store == nil || seed.DefinitionID == "" || seed.VersionID == "" || seed.ProjectID == "" || seed.InstallerUserID == "" {
		return DefinitionRecord{}, fmt.Errorf("definition, version, project and installer user IDs are required")
	}
	definition, err := MinimumLoopDefinition(seed.DefinitionID, seed.InstallerUserID, now)
	if err != nil {
		return DefinitionRecord{}, err
	}
	record := DefinitionRecord{VersionID: seed.VersionID, ProjectID: seed.ProjectID, Key: MinimumLoopKey, Title: "Minimum product delivery loop", Description: "Project brief interview, requirements and blueprint proposals, per-page prototype collaboration, frozen build manifest, workbench build, quality gate, and publish.", Published: seed.Published, Definition: definition}
	if err := store.SaveDefinition(ctx, record); err != nil {
		return DefinitionRecord{}, err
	}
	return record, nil
}

func MinimumLoopDefinition(id, createdBy string, now time.Time) (domain.WorkflowDefinition, error) {
	envelope := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	nodes := []domain.NodeDefinition{
		{ID: "source", Name: "Project brief input", Type: domain.NodeArtifactInput, InputSchema: envelope, OutputSchema: envelope, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, RequireApproved: false, MinimumArtifacts: 1}},
		{ID: "project-brief-edit", Name: "Interview and edit project brief", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, RequiredRole: "editor", Instructions: "Resolve blocking questions with AI assistance and create an exact Project Brief revision."}},
		{ID: "project-brief-review", Name: "Approve project brief", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true}},
		{ID: "requirements-ai", Name: "Generate requirements proposal", Type: domain.NodeAITransform, InputSchema: envelope, OutputSchema: envelope, AITransform: &domain.AITransformNodeConfig{JobType: "derive_requirements", ModelPolicy: "project-default", OutputSchemaVersion: "requirements-proposal/v1", MaxAttempts: 3, Timeout: 5 * time.Minute}},
		{ID: "requirements-edit", Name: "Edit requirements", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, RequiredRole: "editor", Instructions: "Resolve questions and produce stable requirement and acceptance IDs without bypassing the proposal review."}},
		{ID: "requirements-review", Name: "Approve requirements", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true}},
		{ID: "blueprint-ai", Name: "Compile baseline and generate blueprint proposal", Type: domain.NodeAITransform, InputSchema: envelope, OutputSchema: envelope, AITransform: &domain.AITransformNodeConfig{JobType: "decompose_pages", ModelPolicy: "project-default", OutputSchemaVersion: "blueprint-proposal/v1", MaxAttempts: 3, Timeout: 5 * time.Minute}},
		{ID: "blueprint-edit", Name: "Edit blueprint and PageSpecs", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactBlueprint, RequiredRole: "editor", Instructions: "Review the proposal, close coverage gaps, and create exact Blueprint and PageSpec revisions."}},
		{ID: "blueprint-review", Name: "Review blueprint proposal", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true}},
		{ID: "pages", Name: "Create page delivery slices", Type: domain.NodeFanOut, InputSchema: envelope, OutputSchema: envelope, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/workflowContext/deliverySlices", SliceKeyPath: "/key", MergeNodeID: "pages-merged", MaxParallel: 4, ItemKind: "delivery_slice"}},
		{ID: "prototype-ai", Name: "Generate page prototype proposal", Type: domain.NodeAITransform, InputSchema: envelope, OutputSchema: envelope, AITransform: &domain.AITransformNodeConfig{JobType: "generate_prototype", ModelPolicy: "project-default", OutputSchemaVersion: "prototype-proposal/v1", MaxAttempts: 3, Timeout: 5 * time.Minute}},
		{ID: "prototype-edit", Name: "Edit page prototype", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactPrototype, RequiredRole: "editor", Instructions: "Adjust all required responsive states without changing the approved PageSpec."}},
		{ID: "prototype-review", Name: "Approve page prototype", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true, AllowWaiver: false}},
		{ID: "pages-merged", Name: "Merge approved page slices", Type: domain.NodeMerge, InputSchema: envelope, OutputSchema: envelope, Merge: &domain.MergeNodeConfig{FanOutNodeID: "pages", Policy: domain.MergeAll, AllowWaiver: false}},
		{ID: "compile-manifest", Name: "Freeze application build manifest", Type: domain.NodeManifestCompiler, InputSchema: envelope, OutputSchema: envelope, ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}},
		{ID: "workbench", Name: "Build in Workbench", Type: domain.NodeWorkbenchBuild, InputSchema: envelope, OutputSchema: envelope, WorkbenchBuild: &domain.WorkbenchBuildNodeConfig{BuildManifestSchemaVersion: 1, MaxAttempts: 3, Timeout: 15 * time.Minute}},
		{ID: "quality", Name: "Quality gate", Type: domain.NodeQualityGate, InputSchema: envelope, OutputSchema: envelope, QualityGate: &domain.QualityGateNodeConfig{GateName: "release", Blocking: true, RequiredRole: "editor"}},
		{ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: envelope, OutputSchema: envelope, Publish: &domain.PublishNodeConfig{Environment: "production", RequiredRole: "admin", AllowRollback: true}},
	}
	edgePairs := [][2]string{
		{"source", "project-brief-edit"}, {"project-brief-edit", "project-brief-review"},
		{"project-brief-review", "requirements-ai"}, {"requirements-ai", "requirements-edit"},
		{"requirements-edit", "requirements-review"}, {"requirements-review", "blueprint-ai"},
		{"blueprint-ai", "blueprint-edit"}, {"blueprint-edit", "blueprint-review"},
		{"blueprint-review", "pages"}, {"pages", "prototype-ai"},
		{"prototype-ai", "prototype-edit"}, {"prototype-edit", "prototype-review"},
		{"prototype-review", "pages-merged"}, {"pages-merged", "compile-manifest"},
		{"compile-manifest", "workbench"}, {"workbench", "quality"}, {"quality", "publish"},
	}
	edges := make([]domain.WorkflowEdge, len(edgePairs))
	for index, pair := range edgePairs {
		edges[index] = domain.WorkflowEdge{ID: fmt.Sprintf("edge-%02d", index+1), From: pair[0], To: pair[1]}
	}
	return domain.NewWorkflowDefinition(id, 1, "Minimum product delivery loop", "1", nodes, edges, createdBy, now)
}

func MinimumLoopDefinitionJSON(id, createdBy string, now time.Time) ([]byte, error) {
	definition, err := MinimumLoopDefinition(id, createdBy, now)
	if err != nil {
		return nil, err
	}
	return domain.CanonicalJSON(definition)
}
