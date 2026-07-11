package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

// ProvisionBlueprintSelectionFlows installs the immutable selection-to-app
// workflow for projects created before this built-in template existed.
func ProvisionBlueprintSelectionFlows(ctx context.Context, database *gorm.DB, now time.Time) (int, error) {
	if database == nil {
		return 0, fmt.Errorf("gorm database is required")
	}
	store, err := NewGORMStore(database.WithContext(ctx), InlineContentStore{}, nil)
	if err != nil {
		return 0, err
	}
	var projects []storage.ProjectModel
	if err := database.WithContext(ctx).Where("lifecycle = 'active'").Order("id ASC").Find(&projects).Error; err != nil {
		return 0, err
	}
	installed := 0
	for _, project := range projects {
		definitionID, versionID := BlueprintSelectionFlowIDs(project.ID)
		_, getErr := store.GetDefinition(ctx, definitionID, BlueprintSelectionFlowVersion)
		missing := errors.Is(getErr, domain.ErrNotFound)
		if getErr != nil && !missing {
			return installed, getErr
		}
		record, err := SeedBlueprintSelectionFlow(ctx, store, BlueprintSelectionFlowSeed{
			DefinitionID: definitionID, VersionID: versionID, ProjectID: project.ID.String(),
			InstallerUserID: project.CreatedBy.String(), Published: true,
		}, now.UTC())
		if err != nil {
			return installed, fmt.Errorf("provision Blueprint selection workflow for project %s: %w", project.ID, err)
		}
		if missing || !record.Published {
			installed++
		}
	}
	return installed, nil
}

// UpgradeExistingMinimumLoops is an explicit startup provisioner. It runs
// after database migrations, upgrades every project-scoped built-in definition
// to the current immutable version, publishes it, and unpublishes every prior
// version.
// Existing runs remain replayable because they pin a definition version ID.
func UpgradeExistingMinimumLoops(ctx context.Context, database *gorm.DB, now time.Time) (int, error) {
	if database == nil {
		return 0, fmt.Errorf("gorm database is required")
	}
	store, err := NewGORMStore(database.WithContext(ctx), InlineContentStore{}, nil)
	if err != nil {
		return 0, err
	}
	var definitions []definitionRow
	if err := database.WithContext(ctx).
		Where("workflow_key = ? AND lifecycle = 'active' AND project_id IS NOT NULL", MinimumLoopKey).
		Order("id ASC").Find(&definitions).Error; err != nil {
		return 0, err
	}
	installations := make([]minimumLoopInstallation, 0, len(definitions))
	for _, base := range definitions {
		if base.ProjectID == nil {
			continue
		}
		installations = append(installations, minimumLoopInstallation{
			definitionID: base.ID.String(), projectID: base.ProjectID.String(),
			installerUserID: base.CreatedBy.String(),
		})
	}
	return upgradeMinimumLoopInstallations(ctx, store, installations, now)
}

type minimumLoopInstallation struct {
	definitionID    string
	projectID       string
	installerUserID string
}

func upgradeMinimumLoopInstallations(
	ctx context.Context,
	store Store,
	installations []minimumLoopInstallation,
	now time.Time,
) (int, error) {
	upgraded := 0
	for _, installation := range installations {
		if err := ctx.Err(); err != nil {
			return upgraded, err
		}
		current, currentErr := store.GetDefinition(ctx, installation.definitionID, MinimumLoopCurrentVersion)
		needsUpgrade := errors.Is(currentErr, domain.ErrNotFound)
		if currentErr != nil && !needsUpgrade {
			return upgraded, currentErr
		}
		versions, versionsErr := store.ListDefinitionVersions(ctx, installation.definitionID)
		if versionsErr != nil && !errors.Is(versionsErr, domain.ErrNotFound) {
			return upgraded, versionsErr
		}
		needsUnpublish := false
		for _, version := range versions {
			if version.Definition.Version != MinimumLoopCurrentVersion && version.Published {
				needsUnpublish = true
				break
			}
		}
		if currentErr == nil && current.Published && !needsUnpublish {
			continue
		}
		versionID, err := minimumLoopVersionID(installation.projectID, MinimumLoopCurrentVersion)
		if err != nil {
			return upgraded, err
		}
		if _, err := SeedMinimumLoop(ctx, store, MinimumLoopSeed{
			DefinitionID: installation.definitionID, VersionID: versionID,
			ProjectID: installation.projectID, InstallerUserID: installation.installerUserID,
			Published: true,
		}, now.UTC()); err != nil {
			return upgraded, fmt.Errorf("upgrade minimum loop %s: %w", installation.definitionID, err)
		}
		if needsUpgrade || needsUnpublish || !current.Published {
			upgraded++
		}
	}
	return upgraded, nil
}
