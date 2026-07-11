package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

// workflowBuildManifestActivation is the persisted compiler output contract.
// It is intentionally local to core: importing the workflow package here would
// create a core <-> workflow cycle.
type workflowBuildManifestActivation struct {
	SchemaVersion    int                  `json:"schemaVersion"`
	ProjectID        string               `json:"projectId"`
	RunID            string               `json:"runId"`
	ManifestGroupKey string               `json:"manifestGroupKey,omitempty"`
	SliceIDs         []string             `json:"sliceIds"`
	BundleIDs        []string             `json:"bundleIds,omitempty"`
	Sources          []domain.ArtifactRef `json:"sources"`
	Constraints      json.RawMessage      `json:"constraints"`
	CreatedAt        time.Time            `json:"createdAt"`
	Hash             string               `json:"hash"`
}

func (manifest workflowBuildManifestActivation) validate() error {
	if manifest.SchemaVersion < 1 {
		return errors.New("schema version is required")
	}
	for label, value := range map[string]string{
		"project": manifest.ProjectID,
		"run":     manifest.RunID,
		"group":   manifest.ManifestGroupKey,
	} {
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil || parsed.String() != value {
			return fmt.Errorf("%s id is not a canonical UUID", label)
		}
	}
	if len(manifest.SliceIDs) == 0 || len(manifest.SliceIDs) != len(manifest.BundleIDs) {
		return errors.New("one root bundle per delivery slice is required")
	}
	seenSlices := make(map[string]bool, len(manifest.SliceIDs))
	seenBundles := make(map[string]bool, len(manifest.BundleIDs))
	for index := range manifest.SliceIDs {
		sliceID := strings.TrimSpace(manifest.SliceIDs[index])
		bundleID := strings.TrimSpace(manifest.BundleIDs[index])
		parsedBundleID, err := uuid.Parse(bundleID)
		if sliceID == "" || seenSlices[sliceID] || err != nil || parsedBundleID.String() != bundleID || seenBundles[bundleID] {
			return errors.New("slice and root bundle ids must be non-empty and unique")
		}
		seenSlices[sliceID] = true
		seenBundles[bundleID] = true
	}
	if len(manifest.Sources) == 0 {
		return errors.New("pinned artifact sources are required")
	}
	for _, source := range manifest.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
	}
	if len(manifest.Constraints) > 0 {
		if _, err := domain.CanonicalJSON(manifest.Constraints); err != nil {
			return err
		}
	}
	payload := manifest
	expected := payload.Hash
	payload.Hash = ""
	hash, err := domain.CanonicalHash(payload)
	if err != nil {
		return err
	}
	if !domain.IsCanonicalHash(expected) || expected != hash {
		return errors.New("compiler output hash mismatch")
	}
	return nil
}

// ensureWorkflowManifestGroupActivated keeps compiler roots invisible until
// the workflow CAS has atomically persisted both NodeCompleted and the exact,
// hash-verified BuildManifest output. Historical pre-group rows use the
// migration-owned "legacy" marker and cannot be reconstructed, so only that
// exact marker retains compatibility; all current creation paths require a
// compiler-node UUID.
func ensureWorkflowManifestGroupActivated(
	ctx context.Context,
	database *gorm.DB,
	manifest storage.ApplicationBuildManifestModel,
) error {
	if manifest.WorkflowRunID == nil {
		return nil
	}
	if manifest.ManifestGroupKey != nil && *manifest.ManifestGroupKey == "legacy" {
		return nil
	}
	if manifest.ManifestGroupKey == nil || manifest.RootOrdinal == nil || *manifest.RootOrdinal < 0 {
		return workflowManifestGroupNotActivated("workflow root coordinate is incomplete")
	}
	if manifest.DeliverySliceID == nil || strings.TrimSpace(*manifest.DeliverySliceID) == "" {
		return workflowManifestGroupNotActivated("workflow delivery slice identity is incomplete")
	}
	groupID, err := uuid.Parse(strings.TrimSpace(*manifest.ManifestGroupKey))
	if err != nil || groupID.String() != *manifest.ManifestGroupKey {
		return workflowManifestGroupNotActivated("manifest group is not a compiler node UUID")
	}

	var compiler storage.WorkflowNodeRunModel
	if err := database.WithContext(ctx).Select("id", "run_id", "node_key", "node_type", "status", "completed_at").
		Where("id = ? AND run_id = ?", groupID, *manifest.WorkflowRunID).Take(&compiler).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return workflowManifestGroupNotActivated("compiler node is unavailable")
		}
		return err
	}
	if compiler.NodeType != string(domain.NodeManifestCompiler) || compiler.Status != "completed" || compiler.CompletedAt == nil {
		return workflowManifestGroupNotActivated("compiler node has not committed successfully")
	}

	var run storage.WorkflowRunModel
	if err := database.WithContext(ctx).Select("id", "project_id", "context").
		Where("id = ?", *manifest.WorkflowRunID).Take(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return workflowManifestGroupNotActivated("workflow run is unavailable")
		}
		return err
	}
	if run.ProjectID != manifest.ProjectID {
		return workflowManifestGroupNotActivated("workflow run belongs to another project")
	}
	var runContext struct {
		Nodes map[string]struct {
			Output json.RawMessage `json:"output"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(run.Context, &runContext); err != nil {
		return workflowManifestGroupNotActivated("workflow context is invalid")
	}
	metadata, exists := runContext.Nodes[compiler.NodeKey]
	if !exists || len(metadata.Output) == 0 {
		return workflowManifestGroupNotActivated("compiler output is unavailable")
	}
	var output workflowBuildManifestActivation
	if err := json.Unmarshal(metadata.Output, &output); err != nil || output.validate() != nil {
		return workflowManifestGroupNotActivated("compiler output is invalid")
	}
	if output.ProjectID != manifest.ProjectID.String() || output.RunID != manifest.WorkflowRunID.String() ||
		output.ManifestGroupKey != groupID.String() {
		return workflowManifestGroupNotActivated("compiler output coordinate does not match the root")
	}

	var roots []storage.ApplicationBuildManifestModel
	if err := database.WithContext(ctx).Where(
		"project_id = ? AND workflow_run_id = ? AND manifest_group_key = ? AND derived_from_id IS NULL",
		manifest.ProjectID, *manifest.WorkflowRunID, groupID.String(),
	).Order("root_ordinal ASC NULLS LAST, id ASC").Find(&roots).Error; err != nil {
		return err
	}
	if len(roots) != len(output.BundleIDs) {
		return workflowManifestGroupNotActivated("compiler output does not pin the complete root set")
	}
	for ordinal, root := range roots {
		if root.ID.String() != output.BundleIDs[ordinal] || root.RootManifestID != root.ID ||
			root.WorkflowRunID == nil || *root.WorkflowRunID != *manifest.WorkflowRunID ||
			root.ManifestGroupKey == nil || *root.ManifestGroupKey != groupID.String() ||
			root.RootOrdinal == nil || *root.RootOrdinal != ordinal || root.DeliverySliceID == nil ||
			*root.DeliverySliceID != output.SliceIDs[ordinal] {
			return workflowManifestGroupNotActivated("compiler output root order does not match persisted roots")
		}
	}
	if *manifest.RootOrdinal >= len(roots) || roots[*manifest.RootOrdinal].ID != manifest.RootManifestID {
		return workflowManifestGroupNotActivated("requested manifest is outside the committed root set")
	}
	if roots[*manifest.RootOrdinal].DeliverySliceID == nil ||
		*manifest.DeliverySliceID != *roots[*manifest.RootOrdinal].DeliverySliceID {
		return workflowManifestGroupNotActivated("requested manifest delivery slice does not match its committed root")
	}
	return nil
}

// EnsureWorkflowManifestGroupActivated is the shared read barrier for every
// package that exposes an ApplicationBuildManifest. Keeping the implementation
// here prevents delivery and collaboration projections from drifting from the
// Workbench generation/rebase/lineage checks.
func EnsureWorkflowManifestGroupActivated(
	ctx context.Context,
	database *gorm.DB,
	manifest storage.ApplicationBuildManifestModel,
) error {
	if database == nil {
		return workflowManifestGroupNotActivated("activation database is unavailable")
	}
	return ensureWorkflowManifestGroupActivated(ctx, database, manifest)
}

func workflowManifestGroupNotActivated(reason string) error {
	return fmt.Errorf("%w: workflow manifest group is not activated: %s", ErrBlockingGate, reason)
}
