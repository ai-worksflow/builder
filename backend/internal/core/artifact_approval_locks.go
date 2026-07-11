package core

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxApprovalSourceClosureRevisions = 10000

// artifactApprovalLocks is the transaction-local, locked snapshot used by an
// approval decision. The root revision is included alongside every direct and
// transitive immutable revision source.
type artifactApprovalLocks struct {
	artifacts map[uuid.UUID]storage.ArtifactModel
	revisions map[uuid.UUID]storage.ArtifactRevisionModel
	health    map[uuid.UUID]storage.ArtifactHealthModel
}

// lockArtifactApprovalSourceClosure discovers the immutable source closure and
// then locks every referenced artifact, revision, and existing health row. All
// approval transactions use the same table order and UUID order so overlapping
// closures cannot acquire shared rows in opposing orders.
func lockArtifactApprovalSourceClosure(
	ctx context.Context,
	transaction *gorm.DB,
	projectID uuid.UUID,
	rootArtifactID uuid.UUID,
	rootRevisionID uuid.UUID,
) (artifactApprovalLocks, error) {
	revisionArtifacts := map[uuid.UUID]uuid.UUID{rootRevisionID: rootArtifactID}
	visited := make(map[uuid.UUID]struct{})
	frontier := []uuid.UUID{rootRevisionID}

	for len(frontier) > 0 {
		frontier = stableUniqueApprovalUUIDs(frontier)
		unvisited := frontier[:0]
		for _, revisionID := range frontier {
			if _, ok := visited[revisionID]; ok {
				continue
			}
			visited[revisionID] = struct{}{}
			unvisited = append(unvisited, revisionID)
		}
		if len(unvisited) == 0 {
			break
		}

		var sources []storage.ArtifactRevisionSourceModel
		if err := transaction.WithContext(ctx).
			Where("revision_id IN ?", unvisited).
			Order("revision_id ASC, source_revision_id ASC, purpose ASC").
			Find(&sources).Error; err != nil {
			return artifactApprovalLocks{}, fmt.Errorf("load approval source closure: %w", err)
		}

		next := make([]uuid.UUID, 0, len(sources))
		for _, source := range sources {
			if artifactID, exists := revisionArtifacts[source.SourceRevisionID]; exists && artifactID != source.SourceArtifactID {
				return artifactApprovalLocks{}, fmt.Errorf("%w: approval source revision belongs to conflicting artifacts", ErrBlockingGate)
			}
			revisionArtifacts[source.SourceRevisionID] = source.SourceArtifactID
			if _, ok := visited[source.SourceRevisionID]; !ok {
				next = append(next, source.SourceRevisionID)
			}
		}
		if len(revisionArtifacts) > maxApprovalSourceClosureRevisions {
			return artifactApprovalLocks{}, fmt.Errorf("%w: approval source closure exceeds %d revisions", ErrBlockingGate, maxApprovalSourceClosureRevisions)
		}
		frontier = next
	}

	artifactSet := make(map[uuid.UUID]struct{}, len(revisionArtifacts))
	revisionIDs := make([]uuid.UUID, 0, len(revisionArtifacts))
	for revisionID, artifactID := range revisionArtifacts {
		artifactSet[artifactID] = struct{}{}
		revisionIDs = append(revisionIDs, revisionID)
	}
	artifactIDs := make([]uuid.UUID, 0, len(artifactSet))
	for artifactID := range artifactSet {
		artifactIDs = append(artifactIDs, artifactID)
	}
	artifactIDs = stableUniqueApprovalUUIDs(artifactIDs)
	revisionIDs = stableUniqueApprovalUUIDs(revisionIDs)

	var artifactModels []storage.ArtifactModel
	if err := transaction.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", artifactIDs).
		Order("id ASC").
		Find(&artifactModels).Error; err != nil {
		return artifactApprovalLocks{}, fmt.Errorf("lock approval source artifacts: %w", err)
	}
	if len(artifactModels) != len(artifactIDs) {
		return artifactApprovalLocks{}, fmt.Errorf("%w: approval source artifact is missing", ErrBlockingGate)
	}
	artifacts := make(map[uuid.UUID]storage.ArtifactModel, len(artifactModels))
	for _, artifact := range artifactModels {
		if artifact.ProjectID != projectID {
			return artifactApprovalLocks{}, fmt.Errorf("%w: approval source artifact belongs to another project", ErrBlockingGate)
		}
		artifacts[artifact.ID] = artifact
	}

	var revisionModels []storage.ArtifactRevisionModel
	if err := transaction.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", revisionIDs).
		Order("id ASC").
		Find(&revisionModels).Error; err != nil {
		return artifactApprovalLocks{}, fmt.Errorf("lock approval source revisions: %w", err)
	}
	if len(revisionModels) != len(revisionIDs) {
		return artifactApprovalLocks{}, fmt.Errorf("%w: approval source revision is missing", ErrBlockingGate)
	}
	revisions := make(map[uuid.UUID]storage.ArtifactRevisionModel, len(revisionModels))
	for _, revision := range revisionModels {
		expectedArtifactID := revisionArtifacts[revision.ID]
		if revision.ArtifactID != expectedArtifactID {
			return artifactApprovalLocks{}, fmt.Errorf("%w: approval source revision does not belong to its pinned artifact", ErrBlockingGate)
		}
		revisions[revision.ID] = revision
	}

	var healthModels []storage.ArtifactHealthModel
	if err := transaction.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("artifact_id IN ?", artifactIDs).
		Order("artifact_id ASC").
		Find(&healthModels).Error; err != nil {
		return artifactApprovalLocks{}, fmt.Errorf("lock approval source health: %w", err)
	}
	if len(healthModels) != len(artifactIDs) {
		return artifactApprovalLocks{}, fmt.Errorf("%w: approval source health is missing", ErrBlockingGate)
	}
	health := make(map[uuid.UUID]storage.ArtifactHealthModel, len(healthModels))
	for _, model := range healthModels {
		health[model.ArtifactID] = model
	}

	return artifactApprovalLocks{artifacts: artifacts, revisions: revisions, health: health}, nil
}

func stableUniqueApprovalUUIDs(values []uuid.UUID) []uuid.UUID {
	unique := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if _, exists := unique[value]; exists {
			continue
		}
		unique[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].String() < result[right].String()
	})
	return result
}
