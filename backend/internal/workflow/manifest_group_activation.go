package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// validateCompletedManifestCompilerGroupsTx is the publish barrier for a
// ManifestCompiler group. CreateBundle may have inserted several immutable
// roots while the worker was running, but the roots become externally usable
// only through the completed node/output pair committed by this transaction.
func validateCompletedManifestCompilerGroupsTx(
	ctx context.Context,
	transaction *gorm.DB,
	runID uuid.UUID,
	mutation RunMutation,
) error {
	var project struct {
		ProjectID uuid.UUID
	}
	if err := transaction.WithContext(ctx).Model(&runRow{}).Select("project_id").
		Where("id = ?", runID).Take(&project).Error; err != nil {
		return err
	}
	for _, nodeMutation := range mutation.Nodes {
		node := nodeMutation.Node
		if node.Type != domain.NodeManifestCompiler || node.Status != NodeCompleted {
			continue
		}
		metadata, exists := mutation.Context.Nodes[node.Key]
		if !exists || len(metadata.Output) == 0 {
			return fmt.Errorf("completed manifest compiler %s has no frozen output", node.Key)
		}
		var manifest BuildManifest
		if err := json.Unmarshal(metadata.Output, &manifest); err != nil {
			return fmt.Errorf("decode completed manifest compiler %s output: %w", node.Key, err)
		}
		if err := manifest.Validate(); err != nil {
			return fmt.Errorf("completed manifest compiler %s output is invalid: %w", node.Key, err)
		}
		if manifest.ProjectID != project.ProjectID.String() || manifest.RunID != runID.String() || manifest.ManifestGroupKey != node.ID {
			return fmt.Errorf("completed manifest compiler %s output coordinate drifted", node.Key)
		}

		var roots []storage.ApplicationBuildManifestModel
		if err := transaction.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"project_id = ? AND workflow_run_id = ? AND manifest_group_key = ? AND derived_from_id IS NULL",
			project.ProjectID, runID, node.ID,
		).Order("root_ordinal ASC NULLS LAST, id ASC").Find(&roots).Error; err != nil {
			return err
		}
		if len(roots) != len(manifest.BundleIDs) {
			return fmt.Errorf(
				"completed manifest compiler %s pins %d roots but %d immutable roots exist",
				node.Key, len(manifest.BundleIDs), len(roots),
			)
		}
		for ordinal, root := range roots {
			if root.ID.String() != manifest.BundleIDs[ordinal] || root.RootManifestID != root.ID ||
				root.DerivedFromID != nil || root.WorkflowRunID == nil || *root.WorkflowRunID != runID ||
				root.ManifestGroupKey == nil || *root.ManifestGroupKey != node.ID ||
				root.RootOrdinal == nil || *root.RootOrdinal != ordinal || root.Status != "frozen" ||
				root.DeliverySliceID == nil || *root.DeliverySliceID != manifest.SliceIDs[ordinal] {
				return fmt.Errorf("completed manifest compiler %s root order does not match its exact frozen output", node.Key)
			}
		}
	}
	return nil
}
