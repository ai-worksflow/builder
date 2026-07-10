package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	MinimumLoopKey            = "minimum-product-loop"
	MinimumLoopCurrentVersion = 3
	minimumLoopTitle          = "Minimum product delivery loop"
	minimumLoopDescription    = "Project brief interview, requirements and blueprint proposals, per-page prototype collaboration, frozen build manifest, workbench build, quality gate, and publish."
)

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
	if current, err := store.GetDefinition(ctx, seed.DefinitionID, MinimumLoopCurrentVersion); err == nil {
		return ensureSeedPublication(ctx, store, seed, current)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return DefinitionRecord{}, err
	}

	if _, err := store.GetDefinition(ctx, seed.DefinitionID, 1); errors.Is(err, domain.ErrNotFound) {
		legacy, definitionErr := minimumLoopDefinitionV1(seed.DefinitionID, seed.InstallerUserID, now)
		if definitionErr != nil {
			return DefinitionRecord{}, definitionErr
		}
		legacyVersionID, versionErr := minimumLoopVersionID(seed.ProjectID, 1)
		if versionErr != nil {
			return DefinitionRecord{}, versionErr
		}
		legacyRecord := minimumLoopRecord(seed, legacyVersionID, false, legacy)
		if saveErr := store.SaveDefinition(ctx, legacyRecord); saveErr != nil {
			// Memory-backed retries can observe the first version from a prior
			// interrupted install, and concurrent installs race on the same IDs.
			if _, reloadErr := store.GetDefinition(ctx, seed.DefinitionID, 1); reloadErr != nil {
				return DefinitionRecord{}, saveErr
			}
		}
	} else if err != nil {
		return DefinitionRecord{}, err
	}

	for version := 2; version < MinimumLoopCurrentVersion; version++ {
		if _, err := store.GetDefinition(ctx, seed.DefinitionID, version); err == nil {
			continue
		} else if !errors.Is(err, domain.ErrNotFound) {
			return DefinitionRecord{}, err
		}
		definition, definitionErr := minimumLoopDefinitionForVersion(seed.DefinitionID, version, seed.InstallerUserID, now)
		if definitionErr != nil {
			return DefinitionRecord{}, definitionErr
		}
		versionID, versionErr := minimumLoopVersionID(seed.ProjectID, version)
		if versionErr != nil {
			return DefinitionRecord{}, versionErr
		}
		record := minimumLoopRecord(seed, versionID, false, definition)
		if saveErr := store.SaveDefinition(ctx, record); saveErr != nil {
			if _, reloadErr := store.GetDefinition(ctx, seed.DefinitionID, version); reloadErr != nil {
				return DefinitionRecord{}, saveErr
			}
		}
	}

	definition, err := MinimumLoopDefinition(seed.DefinitionID, seed.InstallerUserID, now)
	if err != nil {
		return DefinitionRecord{}, err
	}
	record := minimumLoopRecord(seed, seed.VersionID, seed.Published, definition)
	if err := store.SaveDefinition(ctx, record); err != nil {
		if current, reloadErr := store.GetDefinition(ctx, seed.DefinitionID, MinimumLoopCurrentVersion); reloadErr == nil {
			return ensureSeedPublication(ctx, store, seed, current)
		}
		return DefinitionRecord{}, err
	}
	return ensureSeedPublication(ctx, store, seed, record)
}

func minimumLoopRecord(seed MinimumLoopSeed, versionID string, published bool, definition domain.WorkflowDefinition) DefinitionRecord {
	return DefinitionRecord{
		VersionID: versionID, ProjectID: seed.ProjectID, Key: MinimumLoopKey,
		Title: minimumLoopTitle, Description: minimumLoopDescription,
		Published: published, Definition: definition,
	}
}

func ensureSeedPublication(ctx context.Context, store Store, seed MinimumLoopSeed, record DefinitionRecord) (DefinitionRecord, error) {
	if record.ProjectID != seed.ProjectID || record.Key != MinimumLoopKey || record.Definition.ID != seed.DefinitionID {
		return DefinitionRecord{}, ErrCASConflict
	}
	if seed.Published && !record.Published {
		published, err := store.PublishDefinitionVersion(ctx, seed.ProjectID, seed.DefinitionID, record.VersionID, seed.InstallerUserID)
		if err != nil {
			return DefinitionRecord{}, err
		}
		record = published
	}
	if seed.Published && record.Published && record.Definition.Version > 1 {
		versions, err := store.ListDefinitionVersions(ctx, seed.DefinitionID)
		if err != nil {
			return DefinitionRecord{}, err
		}
		for _, legacy := range versions {
			if legacy.VersionID == record.VersionID || !legacy.Published {
				continue
			}
			if _, err := store.UnpublishDefinitionVersion(ctx, seed.ProjectID, seed.DefinitionID, legacy.VersionID, seed.InstallerUserID); err != nil {
				return DefinitionRecord{}, err
			}
		}
	}
	return record, nil
}

func minimumLoopVersionID(projectID string, version int) (string, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return "", fmt.Errorf("minimum loop project ID must be a UUID: %w", err)
	}
	return uuid.NewSHA1(projectUUID, []byte(fmt.Sprintf("worksflow:minimum-loop:version:%d", version))).String(), nil
}

func MinimumLoopDefinition(id, createdBy string, now time.Time) (domain.WorkflowDefinition, error) {
	return minimumLoopDefinitionForVersion(id, MinimumLoopCurrentVersion, createdBy, now)
}

func minimumLoopDefinitionForVersion(id string, version int, createdBy string, now time.Time) (domain.WorkflowDefinition, error) {
	switch version {
	case 1:
		return minimumLoopDefinitionV1(id, createdBy, now)
	case 2:
		return minimumLoopDefinitionCurrent(id, 2, "2", createdBy, now)
	case MinimumLoopCurrentVersion:
		return minimumLoopDefinitionCurrent(id, MinimumLoopCurrentVersion, "3", createdBy, now)
	default:
		return domain.WorkflowDefinition{}, fmt.Errorf("unsupported minimum loop version %d", version)
	}
}

func minimumLoopDefinitionCurrent(id string, version int, schemaVersion string, createdBy string, now time.Time) (domain.WorkflowDefinition, error) {
	envelope := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	nodes := []domain.NodeDefinition{
		{ID: "source", Name: "Project brief input", Type: domain.NodeArtifactInput, InputSchema: envelope, OutputSchema: envelope, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, AllowedKinds: []string{"project_brief"}, RequireApproved: false, MinimumArtifacts: 1}},
		{ID: "project-brief-ai", Name: "Refine project brief proposal", Type: domain.NodeAITransform, InputSchema: envelope, OutputSchema: envelope, AITransform: &domain.AITransformNodeConfig{JobType: "refine_project_brief", ModelPolicy: "project-default", OutputSchemaVersion: "project-brief-proposal/v1", MaxAttempts: 3, Timeout: 5 * time.Minute}},
		{ID: "project-brief-edit", Name: "Interview and edit project brief", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, ArtifactKind: "project_brief", RequiredRole: "editor", Instructions: "Review the AI proposal, resolve blocking questions, and create an exact Project Brief revision."}},
		{ID: "project-brief-review", Name: "Approve project brief", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true}},
		{ID: "requirements-ai", Name: "Generate requirements proposal", Type: domain.NodeAITransform, InputSchema: envelope, OutputSchema: envelope, AITransform: &domain.AITransformNodeConfig{JobType: "derive_requirements", ModelPolicy: "project-default", OutputSchemaVersion: "requirements-proposal/v1", MaxAttempts: 3, Timeout: 5 * time.Minute}},
		{ID: "requirements-edit", Name: "Edit requirements", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, ArtifactKind: "product_requirements", RequiredRole: "editor", Instructions: "Resolve questions and produce stable requirement and acceptance IDs without bypassing the proposal review."}},
		{ID: "requirements-review", Name: "Approve requirements", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true}},
		{ID: "blueprint-ai", Name: "Compile baseline and generate blueprint proposal", Type: domain.NodeAITransform, InputSchema: envelope, OutputSchema: envelope, AITransform: &domain.AITransformNodeConfig{JobType: "decompose_pages", ModelPolicy: "project-default", OutputSchemaVersion: "blueprint-proposal/v1", MaxAttempts: 3, Timeout: 5 * time.Minute}},
		{ID: "blueprint-edit", Name: "Edit blueprint", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactBlueprint, ArtifactKind: "blueprint", RequiredRole: "editor", Instructions: "Review the proposal, close coverage gaps, and create one exact Blueprint revision. PageSpecs are created after Blueprint approval."}},
		{ID: "blueprint-review", Name: "Approve blueprint", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true}},
		// blueprint_page is a specialized resolver contract: the runner derives
		// one branch per page from this node's exact approved Blueprint input and
		// anchors every emitted slice back to that immutable Blueprint revision.
		{ID: "pages", Name: "Create Blueprint page branches", Type: domain.NodeFanOut, InputSchema: envelope, OutputSchema: envelope, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/blueprintPages", SliceKeyPath: "/key", MergeNodeID: "pages-merged", MaxParallel: 4, ItemKind: "blueprint_page"}},
		{ID: "page-spec-ai", Name: "Generate page specification proposal", Type: domain.NodeAITransform, InputSchema: envelope, OutputSchema: envelope, AITransform: &domain.AITransformNodeConfig{JobType: "generate_page_spec", ModelPolicy: "project-default", OutputSchemaVersion: "page-spec-proposal/v1", MaxAttempts: 3, Timeout: 5 * time.Minute}},
		{ID: "page-spec-edit", Name: "Edit page specification", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactBlueprint, ArtifactKind: "page_spec", RequiredRole: "editor", Instructions: "Review the proposal and create one exact PageSpec revision anchored to this branch's approved Blueprint page."}},
		{ID: "page-spec-review", Name: "Approve page specification", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true, AllowWaiver: false}},
		{ID: "prototype-ai", Name: "Generate page prototype proposal", Type: domain.NodeAITransform, InputSchema: envelope, OutputSchema: envelope, AITransform: &domain.AITransformNodeConfig{JobType: "generate_prototype", ModelPolicy: "project-default", OutputSchemaVersion: "prototype-proposal/v1", MaxAttempts: 3, Timeout: 5 * time.Minute}},
		{ID: "prototype-edit", Name: "Edit page prototype", Type: domain.NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactPrototype, ArtifactKind: "prototype", RequiredRole: "editor", Instructions: "Adjust all required responsive states without changing the approved PageSpec."}},
		{ID: "prototype-review", Name: "Approve page prototype", Type: domain.NodeReviewGate, InputSchema: envelope, OutputSchema: envelope, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true, AllowWaiver: false}},
		{ID: "pages-merged", Name: "Merge approved page branches", Type: domain.NodeMerge, InputSchema: envelope, OutputSchema: envelope, Merge: &domain.MergeNodeConfig{FanOutNodeID: "pages", Policy: domain.MergeAll, AllowWaiver: false}},
		{ID: "compile-manifest", Name: "Freeze application build manifest", Type: domain.NodeManifestCompiler, InputSchema: envelope, OutputSchema: envelope, ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}},
		{ID: "workbench", Name: "Build in Workbench", Type: domain.NodeWorkbenchBuild, InputSchema: envelope, OutputSchema: envelope, WorkbenchBuild: &domain.WorkbenchBuildNodeConfig{BuildManifestSchemaVersion: 1, MaxAttempts: 3, Timeout: 15 * time.Minute}},
		{ID: "quality", Name: "Quality gate", Type: domain.NodeQualityGate, InputSchema: envelope, OutputSchema: envelope, QualityGate: &domain.QualityGateNodeConfig{GateName: "release", Blocking: true, RequiredRole: "editor"}},
		{ID: "publish", Name: "Publish", Type: domain.NodePublish, InputSchema: envelope, OutputSchema: envelope, Publish: &domain.PublishNodeConfig{Environment: "production", RequiredRole: "admin", AllowRollback: true}},
	}
	edgePairs := [][2]string{
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
	edges := make([]domain.WorkflowEdge, len(edgePairs))
	for index, pair := range edgePairs {
		edges[index] = domain.WorkflowEdge{ID: fmt.Sprintf("edge-%02d", index+1), From: pair[0], To: pair[1]}
	}
	return domain.NewWorkflowDefinitionWithContracts(
		id, version, minimumLoopTitle, schemaVersion, nodes, edges,
		ProjectBriefInputContract(), ApplicationOutputContract(), createdBy, now,
	)
}

func minimumLoopDefinitionV1(id, createdBy string, now time.Time) (domain.WorkflowDefinition, error) {
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
