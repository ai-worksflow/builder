package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
)

type ExactTreeReader interface {
	Get(context.Context, string, string, repository.TreeBlobPointer) (repository.TreeManifest, error)
}

// PostgresCandidateTreeResolver resolves an immutable historical tree by its
// semantic hash. Candidate current rows may advance while an Attempt waits;
// base snapshots and append-only journal after-pointers keep every prior tree
// reachable without copying the whole repository into the TaskCapsule.
type PostgresCandidateTreeResolver struct {
	database *gorm.DB
	trees    ExactTreeReader
}

func NewPostgresCandidateTreeResolver(
	database *gorm.DB,
	trees ExactTreeReader,
) (*PostgresCandidateTreeResolver, error) {
	if database == nil || trees == nil {
		return nil, fmt.Errorf("%w: Candidate tree database and content reader are required", ErrExecutionBlocked)
	}
	return &PostgresCandidateTreeResolver{database: database, trees: trees}, nil
}

type exactTreePointerRow struct {
	Store             string `gorm:"column:tree_store"`
	OwnerID           string `gorm:"column:tree_owner_id"`
	Ref               string `gorm:"column:tree_ref"`
	ContentObjectHash string `gorm:"column:tree_content_hash"`
	TreeHash          string `gorm:"column:tree_hash"`
	FileCount         int    `gorm:"column:tree_file_count"`
	ByteSize          int64  `gorm:"column:tree_byte_size"`
}

func (resolver *PostgresCandidateTreeResolver) ResolveExactTree(
	ctx context.Context,
	capsule TaskCapsule,
) (repository.TreeManifest, error) {
	if resolver == nil || ctx == nil || !validUUIDs(capsule.ProjectID, capsule.CandidateID) ||
		!sha256Pattern.MatchString(capsule.BaseCandidateTreeHash) {
		return repository.TreeManifest{}, fmt.Errorf("%w: exact Candidate tree identity", ErrExecutionBlocked)
	}
	var pointers []exactTreePointerRow
	result := resolver.database.WithContext(ctx).Raw(`
WITH exact_candidate AS (
  SELECT id, project_id, repository_snapshot_id
  FROM candidate_workspaces
  WHERE id = ? AND project_id = ?
), tree_pointers AS (
  SELECT 1 AS priority,
         candidate.current_tree_store AS tree_store,
         candidate.current_tree_owner_id::text AS tree_owner_id,
         candidate.current_tree_ref AS tree_ref,
         candidate.current_tree_content_hash AS tree_content_hash,
         candidate.current_tree_hash AS tree_hash,
         candidate.current_tree_file_count AS tree_file_count,
         candidate.current_tree_byte_size AS tree_byte_size
  FROM candidate_workspaces AS candidate
  JOIN exact_candidate ON exact_candidate.id = candidate.id
  WHERE candidate.current_tree_hash = ?

  UNION ALL

  SELECT 2 AS priority,
         snapshot.tree_store,
         snapshot.tree_owner_id::text,
         snapshot.tree_ref,
         snapshot.tree_content_hash,
         snapshot.tree_hash,
         snapshot.tree_file_count,
         snapshot.tree_byte_size
  FROM repository_snapshots AS snapshot
  JOIN exact_candidate ON exact_candidate.repository_snapshot_id = snapshot.id
                      AND exact_candidate.project_id = snapshot.project_id
  WHERE snapshot.tree_hash = ?

  UNION ALL

  SELECT 3 AS priority,
         journal.after_tree_store,
         journal.after_tree_owner_id::text,
         journal.after_tree_ref,
         journal.after_tree_content_hash,
         journal.after_tree_hash,
         journal.after_tree_file_count,
         journal.after_tree_byte_size
  FROM candidate_workspace_journal AS journal
  JOIN exact_candidate ON exact_candidate.id = journal.candidate_id
  WHERE journal.after_tree_hash = ?
)
SELECT tree_store, tree_owner_id, tree_ref, tree_content_hash,
       tree_hash, tree_file_count, tree_byte_size
FROM tree_pointers
ORDER BY priority, tree_ref
`, capsule.CandidateID, capsule.ProjectID, capsule.BaseCandidateTreeHash,
		capsule.BaseCandidateTreeHash, capsule.BaseCandidateTreeHash).Scan(&pointers)
	if result.Error != nil {
		return repository.TreeManifest{}, fmt.Errorf("%w: query exact Candidate tree: %v", ErrExecutionBlocked, result.Error)
	}
	if len(pointers) == 0 {
		return repository.TreeManifest{}, fmt.Errorf("%w: exact historical Candidate tree is unavailable", ErrExecutionBlocked)
	}
	var selected repository.TreeManifest
	for index, row := range pointers {
		pointer := repository.TreeBlobPointer{
			Store: strings.TrimSpace(row.Store), Ref: strings.TrimSpace(row.Ref),
			OwnerID: strings.TrimSpace(row.OwnerID), TreeHash: row.TreeHash,
			FileCount: row.FileCount, ByteSize: row.ByteSize, ContentObjectHash: row.ContentObjectHash,
		}
		tree, err := resolver.trees.Get(ctx, capsule.ProjectID, pointer.OwnerID, pointer)
		if err != nil {
			return repository.TreeManifest{}, fmt.Errorf("%w: read exact Candidate tree: %v", ErrExecutionBlocked, err)
		}
		tree, err = repository.ParseTree(tree)
		if err != nil || tree.TreeHash != capsule.BaseCandidateTreeHash ||
			len(tree.Files) != pointer.FileCount || worktreeTreeByteSize(tree) != pointer.ByteSize {
			return repository.TreeManifest{}, fmt.Errorf("%w: historical Candidate tree pointer drifted", ErrExecutionDrift)
		}
		if index == 0 {
			selected = tree
			continue
		}
		if !equalJSON(selected, tree) {
			return repository.TreeManifest{}, fmt.Errorf("%w: one tree hash resolves to conflicting manifests", ErrExecutionDrift)
		}
	}
	return selected, nil
}

func worktreeTreeByteSize(tree repository.TreeManifest) int64 {
	total := int64(0)
	for _, file := range tree.Files {
		total += file.ByteSize
	}
	return total
}
