package workflow

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MinimumLoopProjectInitializer installs the built-in template in the same
// transaction as the project, owner membership, and initial Project Brief.
// Workflow list endpoints therefore remain side-effect free.
type MinimumLoopProjectInitializer struct{}

func (MinimumLoopProjectInitializer) InitializeProject(
	ctx context.Context,
	transaction *gorm.DB,
	projectID uuid.UUID,
	actorID uuid.UUID,
	createdAt time.Time,
) error {
	store, err := NewGORMStore(transaction.WithContext(ctx), InlineContentStore{}, nil)
	if err != nil {
		return err
	}
	definitionID := uuid.NewSHA1(projectID, []byte("worksflow:minimum-loop:definition")).String()
	versionID, err := minimumLoopVersionID(projectID.String(), MinimumLoopCurrentVersion)
	if err != nil {
		return err
	}
	_, err = SeedMinimumLoop(ctx, store, MinimumLoopSeed{
		DefinitionID: definitionID, VersionID: versionID, ProjectID: projectID.String(),
		InstallerUserID: actorID.String(), Published: true,
	}, createdAt.UTC())
	if err != nil {
		return err
	}
	selectionDefinitionID, selectionVersionID := BlueprintSelectionFlowIDs(projectID)
	_, err = SeedBlueprintSelectionFlow(ctx, store, BlueprintSelectionFlowSeed{
		DefinitionID: selectionDefinitionID, VersionID: selectionVersionID,
		ProjectID: projectID.String(), InstallerUserID: actorID.String(), Published: true,
	}, createdAt.UTC())
	return err
}
