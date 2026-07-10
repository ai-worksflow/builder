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
	BlueprintSelectionFlowKey     = "blueprint-selection-app"
	BlueprintSelectionFlowVersion = 2
)

type BlueprintSelectionFlowSeed struct {
	DefinitionID    string
	VersionID       string
	ProjectID       string
	InstallerUserID string
	Published       bool
}

func SeedBlueprintSelectionFlow(
	ctx context.Context,
	store Store,
	seed BlueprintSelectionFlowSeed,
	now time.Time,
) (DefinitionRecord, error) {
	if store == nil || seed.DefinitionID == "" || seed.VersionID == "" || seed.ProjectID == "" || seed.InstallerUserID == "" {
		return DefinitionRecord{}, fmt.Errorf("Blueprint selection definition, version, project and installer IDs are required")
	}
	if existing, err := store.GetDefinition(ctx, seed.DefinitionID, BlueprintSelectionFlowVersion); err == nil {
		if existing.ProjectID != seed.ProjectID || existing.Key != BlueprintSelectionFlowKey {
			return DefinitionRecord{}, ErrCASConflict
		}
		return ensureBlueprintSelectionPublication(ctx, store, seed, existing)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return DefinitionRecord{}, err
	}
	if _, err := store.GetDefinition(ctx, seed.DefinitionID, 1); errors.Is(err, domain.ErrNotFound) {
		legacy, definitionErr := blueprintSelectionFlowDefinitionForVersion(seed.DefinitionID, 1, seed.InstallerUserID, now)
		if definitionErr != nil {
			return DefinitionRecord{}, definitionErr
		}
		versionID, versionErr := blueprintSelectionFlowVersionID(seed.ProjectID, 1)
		if versionErr != nil {
			return DefinitionRecord{}, versionErr
		}
		legacyRecord := DefinitionRecord{
			VersionID: versionID, ProjectID: seed.ProjectID, Key: BlueprintSelectionFlowKey,
			Title:       "Build application from Blueprint selection",
			Description: "Fan out only the approved pages frozen by a Blueprint selection, compile an isolated application manifest, build in Workbench, verify, and publish.",
			Published:   false, Definition: legacy,
		}
		if saveErr := store.SaveDefinition(ctx, legacyRecord); saveErr != nil {
			if _, reloadErr := store.GetDefinition(ctx, seed.DefinitionID, 1); reloadErr != nil {
				return DefinitionRecord{}, saveErr
			}
		}
	} else if err != nil {
		return DefinitionRecord{}, err
	}
	definition, err := BlueprintSelectionFlowDefinition(seed.DefinitionID, seed.InstallerUserID, now)
	if err != nil {
		return DefinitionRecord{}, err
	}
	record := DefinitionRecord{
		VersionID: seed.VersionID, ProjectID: seed.ProjectID, Key: BlueprintSelectionFlowKey,
		Title:       "Build application from Blueprint selection",
		Description: "Fan out only the approved pages frozen by a Blueprint selection, compile an isolated application manifest, build in Workbench, verify, and publish.",
		Published:   seed.Published, Definition: definition,
	}
	if err := store.SaveDefinition(ctx, record); err != nil {
		if existing, reloadErr := store.GetDefinition(ctx, seed.DefinitionID, BlueprintSelectionFlowVersion); reloadErr == nil {
			return existing, nil
		}
		return DefinitionRecord{}, err
	}
	return ensureBlueprintSelectionPublication(ctx, store, seed, record)
}

func ensureBlueprintSelectionPublication(
	ctx context.Context,
	store Store,
	seed BlueprintSelectionFlowSeed,
	record DefinitionRecord,
) (DefinitionRecord, error) {
	if seed.Published && !record.Published {
		published, err := store.PublishDefinitionVersion(ctx, seed.ProjectID, seed.DefinitionID, record.VersionID, seed.InstallerUserID)
		if err != nil {
			return DefinitionRecord{}, err
		}
		record = published
	}
	if !seed.Published || !record.Published {
		return record, nil
	}
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
	return record, nil
}

func BlueprintSelectionFlowIDs(projectID uuid.UUID) (definitionID, versionID string) {
	return uuid.NewSHA1(projectID, []byte("worksflow:blueprint-selection:definition")).String(),
		uuid.NewSHA1(projectID, []byte(fmt.Sprintf("worksflow:blueprint-selection:version:%d", BlueprintSelectionFlowVersion))).String()
}

func blueprintSelectionFlowVersionID(projectID string, version int) (string, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return "", fmt.Errorf("Blueprint selection project ID must be a UUID: %w", err)
	}
	return uuid.NewSHA1(projectUUID, []byte(fmt.Sprintf("worksflow:blueprint-selection:version:%d", version))).String(), nil
}

func BlueprintSelectionFlowDefinition(id, createdBy string, now time.Time) (domain.WorkflowDefinition, error) {
	return blueprintSelectionFlowDefinitionForVersion(id, BlueprintSelectionFlowVersion, createdBy, now)
}

func blueprintSelectionFlowDefinitionForVersion(id string, version int, createdBy string, now time.Time) (domain.WorkflowDefinition, error) {
	schemaVersion := fmt.Sprintf("%d", version)
	envelope := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	nodes := []domain.NodeDefinition{
		{ID: "selection", Name: "Frozen Blueprint selection", Type: domain.NodeArtifactInput, InputSchema: envelope, OutputSchema: envelope, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactBlueprint}, AllowedKinds: []string{"blueprint"}, RequireApproved: true, MinimumArtifacts: 1}},
		{ID: "pages", Name: "Selected approved pages", Type: domain.NodeFanOut, InputSchema: envelope, OutputSchema: envelope, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/blueprintPages", SliceKeyPath: "/key", MergeNodeID: "pages-merged", MaxParallel: 4, ItemKind: "blueprint_selection_page"}},
		{ID: "page-ready", Name: "Use frozen PageSpec and Prototype", Type: domain.NodeTransform, InputSchema: envelope, OutputSchema: envelope, Transform: &domain.TransformNodeConfig{Transform: "selection_passthrough"}},
		{ID: "pages-merged", Name: "Merge selected page branches", Type: domain.NodeMerge, InputSchema: envelope, OutputSchema: envelope, Merge: &domain.MergeNodeConfig{FanOutNodeID: "pages", Policy: domain.MergeAll, AllowWaiver: false}},
		{ID: "compile-manifest", Name: "Compile isolated selection manifest", Type: domain.NodeManifestCompiler, InputSchema: envelope, OutputSchema: envelope, ManifestCompiler: &domain.ManifestCompilerNodeConfig{ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1"}},
		{ID: "workbench", Name: "Build selection in Workbench", Type: domain.NodeWorkbenchBuild, InputSchema: envelope, OutputSchema: envelope, WorkbenchBuild: &domain.WorkbenchBuildNodeConfig{BuildManifestSchemaVersion: 1, MaxAttempts: 3, Timeout: 15 * time.Minute}},
		{ID: "quality", Name: "Selection quality gate", Type: domain.NodeQualityGate, InputSchema: envelope, OutputSchema: envelope, QualityGate: &domain.QualityGateNodeConfig{GateName: "release", Blocking: true, RequiredRole: "editor"}},
		{ID: "publish", Name: "Publish selection", Type: domain.NodePublish, InputSchema: envelope, OutputSchema: envelope, Publish: &domain.PublishNodeConfig{Environment: "production", RequiredRole: "admin", AllowRollback: true}},
	}
	pairs := [][2]string{{"selection", "pages"}, {"pages", "page-ready"}, {"page-ready", "pages-merged"}, {"pages-merged", "compile-manifest"}, {"compile-manifest", "workbench"}, {"workbench", "quality"}, {"quality", "publish"}}
	edges := make([]domain.WorkflowEdge, len(pairs))
	for index, pair := range pairs {
		edges[index] = domain.WorkflowEdge{ID: fmt.Sprintf("edge-%02d", index+1), From: pair[0], To: pair[1]}
	}
	return domain.NewWorkflowDefinitionWithContracts(
		id, version, "Build application from Blueprint selection", schemaVersion, nodes, edges,
		BlueprintSelectionInputContract(), ApplicationOutputContract(), createdBy, now,
	)
}
