package repository

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	CandidateRebaseSchemaVersion         = "candidate-rebase/v1"
	CandidateRebasePlanSchemaVersion     = "candidate-rebase-plan/v1"
	CandidateRebaseConflictSchemaVersion = "candidate-rebase-conflict/v1"
)

var ErrInvalidRebase = errors.New("invalid repository Candidate rebase")

type CandidateRebaseState string

const (
	CandidateRebaseApplying   CandidateRebaseState = "applying"
	CandidateRebaseConflicted CandidateRebaseState = "conflicted"
	CandidateRebaseReady      CandidateRebaseState = "ready"
)

type CandidateRebaseOperation struct {
	Ordinal   int           `json:"ordinal"`
	Operation FileOperation `json:"operation"`
}

type CandidateRebaseConflict struct {
	SchemaVersion   string    `json:"schemaVersion"`
	ID              string    `json:"id"`
	Ordinal         int       `json:"ordinal"`
	Path            string    `json:"path"`
	AncestorFile    *TreeFile `json:"ancestorFile"`
	PredecessorFile *TreeFile `json:"predecessorFile"`
	TargetFile      *TreeFile `json:"targetFile"`
}

type CandidateRebasePlan struct {
	SchemaVersion       string                     `json:"schemaVersion"`
	RebaseID            string                     `json:"rebaseId"`
	AncestorTreeHash    string                     `json:"ancestorTreeHash"`
	PredecessorTreeHash string                     `json:"predecessorTreeHash"`
	TargetTreeHash      string                     `json:"targetTreeHash"`
	PlannedTreeHash     string                     `json:"plannedTreeHash"`
	PlanHash            string                     `json:"planHash"`
	Operations          []CandidateRebaseOperation `json:"operations"`
	Conflicts           []CandidateRebaseConflict  `json:"conflicts"`
}

type CandidateRebaseConflictState string

const (
	CandidateRebaseConflictOpen     CandidateRebaseConflictState = "open"
	CandidateRebaseConflictResolved CandidateRebaseConflictState = "resolved"
)

type CandidateRebaseResolutionStrategy string

const (
	CandidateRebaseUsePredecessor CandidateRebaseResolutionStrategy = "predecessor"
	CandidateRebaseUseTarget      CandidateRebaseResolutionStrategy = "target"
	CandidateRebaseUseCurrent     CandidateRebaseResolutionStrategy = "current"
)

type CandidateRebaseConflictRecord struct {
	CandidateRebaseConflict
	State              CandidateRebaseConflictState       `json:"state"`
	Version            uint64                             `json:"version"`
	ResolutionStrategy *CandidateRebaseResolutionStrategy `json:"resolutionStrategy,omitempty"`
	ResolutionFile     *TreeFile                          `json:"resolutionFile,omitempty"`
	ResolutionDeleted  *bool                              `json:"resolutionDeleted,omitempty"`
	ResolvedBy         string                             `json:"resolvedBy,omitempty"`
	ResolvedAt         *time.Time                         `json:"resolvedAt,omitempty"`
	CreatedAt          time.Time                          `json:"createdAt"`
}

type CandidateRebase struct {
	SchemaVersion          string                          `json:"schemaVersion"`
	ID                     string                          `json:"id"`
	ProjectID              string                          `json:"projectId"`
	OperationID            string                          `json:"operationId"`
	PredecessorCandidateID string                          `json:"predecessorCandidateId"`
	SuccessorCandidateID   string                          `json:"successorCandidateId"`
	TargetBuildManifestID  string                          `json:"targetBuildManifestId"`
	AncestorTreeHash       string                          `json:"ancestorTreeHash"`
	PredecessorTreeHash    string                          `json:"predecessorTreeHash"`
	TargetTreeHash         string                          `json:"targetTreeHash"`
	PlannedTreeHash        string                          `json:"plannedTreeHash"`
	PlanHash               string                          `json:"planHash"`
	State                  CandidateRebaseState            `json:"state"`
	Version                uint64                          `json:"version"`
	Operations             []CandidateRebaseOperation      `json:"operations"`
	Conflicts              []CandidateRebaseConflictRecord `json:"conflicts"`
	CreatedBy              string                          `json:"createdBy"`
	CreatedAt              time.Time                       `json:"createdAt"`
	UpdatedAt              time.Time                       `json:"updatedAt"`
}

// PlanCandidateRebase performs a deterministic file-level three-way merge.
// It deliberately does not guess at textual merges: when predecessor and
// target both changed the same path differently, the target file remains in
// the successor and an explicit conflict is returned for a user decision.
func PlanCandidateRebase(
	rebaseID string,
	ancestor, predecessor, target TreeManifest,
) (CandidateRebasePlan, error) {
	if !validUUID(rebaseID) {
		return CandidateRebasePlan{}, fmt.Errorf("%w: rebase ID", ErrInvalidRebase)
	}
	var err error
	if ancestor, err = ParseTree(ancestor); err != nil {
		return CandidateRebasePlan{}, fmt.Errorf("%w: ancestor tree: %v", ErrInvalidRebase, err)
	}
	if predecessor, err = ParseTree(predecessor); err != nil {
		return CandidateRebasePlan{}, fmt.Errorf("%w: predecessor tree: %v", ErrInvalidRebase, err)
	}
	if target, err = ParseTree(target); err != nil {
		return CandidateRebasePlan{}, fmt.Errorf("%w: target tree: %v", ErrInvalidRebase, err)
	}

	ancestorFiles := treeFilesByPath(ancestor)
	predecessorFiles := treeFilesByPath(predecessor)
	targetFiles := treeFilesByPath(target)
	paths := mergedTreePaths(ancestorFiles, predecessorFiles, targetFiles)

	operations := make([]CandidateRebaseOperation, 0)
	conflicts := make([]CandidateRebaseConflict, 0)
	planned := target
	for _, path := range paths {
		ancestorFile := treeFileAt(ancestorFiles, path)
		predecessorFile := treeFileAt(predecessorFiles, path)
		targetFile := treeFileAt(targetFiles, path)

		var selected *TreeFile
		switch {
		case equalTreeFile(predecessorFile, ancestorFile):
			selected = targetFile
		case equalTreeFile(targetFile, ancestorFile):
			selected = predecessorFile
		case equalTreeFile(predecessorFile, targetFile):
			selected = predecessorFile
		default:
			conflicts = append(conflicts, CandidateRebaseConflict{
				SchemaVersion: CandidateRebaseConflictSchemaVersion,
				ID:            deterministicRebaseUUID(rebaseID, "conflict", path),
				Ordinal:       len(conflicts), Path: path,
				AncestorFile:    cloneTreeFile(ancestorFile),
				PredecessorFile: cloneTreeFile(predecessorFile),
				TargetFile:      cloneTreeFile(targetFile),
			})
			continue
		}
		if equalTreeFile(selected, targetFile) {
			continue
		}
		operation := rebaseFileOperation(rebaseID, len(operations), path, targetFile, selected)
		operation, err = NormalizeOperation(operation)
		if err != nil {
			return CandidateRebasePlan{}, fmt.Errorf("%w: derive operation for %s: %v", ErrInvalidRebase, path, err)
		}
		planned, err = ApplyOperation(planned, operation)
		if err != nil {
			return CandidateRebasePlan{}, fmt.Errorf("%w: apply operation for %s: %v", ErrInvalidRebase, path, err)
		}
		operations = append(operations, CandidateRebaseOperation{Ordinal: len(operations), Operation: operation})
	}
	if err := rejectCaseFoldCollisions(planned); err != nil {
		return CandidateRebasePlan{}, err
	}

	plan := CandidateRebasePlan{
		SchemaVersion: CandidateRebasePlanSchemaVersion, RebaseID: strings.TrimSpace(rebaseID),
		AncestorTreeHash: ancestor.TreeHash, PredecessorTreeHash: predecessor.TreeHash,
		TargetTreeHash: target.TreeHash, PlannedTreeHash: planned.TreeHash,
		Operations: operations, Conflicts: conflicts,
	}
	plan.PlanHash, err = candidateRebasePlanHash(plan)
	if err != nil {
		return CandidateRebasePlan{}, err
	}
	return plan, nil
}

func candidateRebasePlanHash(plan CandidateRebasePlan) (string, error) {
	hash, err := domain.CanonicalHash(struct {
		SchemaVersion       string                     `json:"schemaVersion"`
		RebaseID            string                     `json:"rebaseId"`
		AncestorTreeHash    string                     `json:"ancestorTreeHash"`
		PredecessorTreeHash string                     `json:"predecessorTreeHash"`
		TargetTreeHash      string                     `json:"targetTreeHash"`
		PlannedTreeHash     string                     `json:"plannedTreeHash"`
		Operations          []CandidateRebaseOperation `json:"operations"`
		Conflicts           []CandidateRebaseConflict  `json:"conflicts"`
	}{
		SchemaVersion: plan.SchemaVersion, RebaseID: plan.RebaseID,
		AncestorTreeHash: plan.AncestorTreeHash, PredecessorTreeHash: plan.PredecessorTreeHash,
		TargetTreeHash: plan.TargetTreeHash, PlannedTreeHash: plan.PlannedTreeHash,
		Operations: plan.Operations, Conflicts: plan.Conflicts,
	})
	if err != nil {
		return "", fmt.Errorf("%w: hash plan: %v", ErrInvalidRebase, err)
	}
	return "sha256:" + hash, nil
}

func treeFilesByPath(tree TreeManifest) map[string]TreeFile {
	result := make(map[string]TreeFile, len(tree.Files))
	for _, file := range tree.Files {
		result[file.Path] = file
	}
	return result
}

func mergedTreePaths(trees ...map[string]TreeFile) []string {
	set := make(map[string]struct{})
	for _, tree := range trees {
		for path := range tree {
			set[path] = struct{}{}
		}
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func treeFileAt(files map[string]TreeFile, path string) *TreeFile {
	file, found := files[path]
	if !found {
		return nil
	}
	return &file
}

func equalTreeFile(left, right *TreeFile) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneTreeFile(file *TreeFile) *TreeFile {
	if file == nil {
		return nil
	}
	copy := *file
	return &copy
}

func rebaseFileOperation(
	rebaseID string,
	ordinal int,
	path string,
	target, selected *TreeFile,
) FileOperation {
	operation := FileOperation{ID: fmt.Sprintf("rebase:%s:%05d", rebaseID, ordinal), Path: path}
	if selected == nil {
		operation.Kind = OperationDelete
		operation.ExpectedHash = target.ContentHash
		return operation
	}
	operation.Kind = OperationUpsert
	operation.ContentHash = selected.ContentHash
	operation.ByteSize = selected.ByteSize
	operation.Mode = selected.Mode
	if target != nil {
		operation.ExpectedHash = target.ContentHash
	}
	return operation
}

func deterministicRebaseUUID(rebaseID string, values ...string) string {
	payload := append([]string{"candidate-rebase/v1", rebaseID}, values...)
	digest := sha256.Sum256([]byte(strings.Join(payload, "\x00")))
	identifier, _ := uuid.FromBytes(digest[:16])
	identifier[6] = (identifier[6] & 0x0f) | 0x50
	identifier[8] = (identifier[8] & 0x3f) | 0x80
	return identifier.String()
}

func rejectCaseFoldCollisions(tree TreeManifest) error {
	seen := make(map[string]string, len(tree.Files))
	for _, file := range tree.Files {
		key := strings.ToLower(file.Path)
		if previous, found := seen[key]; found && previous != file.Path {
			return fmt.Errorf("%w: merged paths %s and %s collide under case folding", ErrInvalidRebase, previous, file.Path)
		}
		seen[key] = file.Path
	}
	return nil
}
