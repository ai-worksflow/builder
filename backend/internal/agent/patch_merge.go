package agent

import (
	"fmt"

	"github.com/worksflow/builder/backend/internal/repository"
)

const PatchMergeConflictConcurrentChange = "concurrent_change"

// PatchFileState is the exact semantic state of one repository path at a
// point in the three-way merge. An absent file has Exists=false and no other
// fields. Keeping mode and byte size in the comparison prevents a same-digest
// mode change from being silently overwritten.
type PatchFileState struct {
	Exists      bool   `json:"exists"`
	ContentHash string `json:"contentHash,omitempty"`
	ByteSize    int64  `json:"byteSize,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

type PatchMergeConflict struct {
	Path     string         `json:"path"`
	Reason   string         `json:"reason"`
	Base     PatchFileState `json:"base"`
	Current  PatchFileState `json:"current"`
	Proposed PatchFileState `json:"proposed"`
}

// PlatformPatchMergePlan is a deterministic, all-or-nothing three-way merge
// plan. Operations is deliberately empty whenever Conflicts is non-empty, so
// no caller can accidentally apply the conflict-free subset of a patch.
type PlatformPatchMergePlan struct {
	BaseTreeHash     string                     `json:"baseTreeHash"`
	CurrentTreeHash  string                     `json:"currentTreeHash"`
	ProposedTreeHash string                     `json:"proposedTreeHash"`
	PlannedTreeHash  string                     `json:"plannedTreeHash"`
	Operations       []repository.FileOperation `json:"operations"`
	Conflicts        []PatchMergeConflict       `json:"conflicts"`
}

// PlanPlatformPatchMerge compares the exact Attempt base, the current
// Candidate, and the Agent proposal. It never performs a two-way overwrite:
// a path is changed only when the current state still equals the Attempt base.
// A path that already equals the proposal is an idempotent no-op.
func PlanPlatformPatchMerge(
	base repository.TreeManifest,
	current repository.TreeManifest,
	patch PlatformPatch,
	mergeID string,
) (PlatformPatchMergePlan, error) {
	if !validUUIDs(mergeID) {
		return PlatformPatchMergePlan{}, fmt.Errorf("%w: merge identity", ErrEvidenceInvalid)
	}
	base, err := repository.ParseTree(base)
	if err != nil {
		return PlatformPatchMergePlan{}, fmt.Errorf("%w: merge base tree: %v", ErrExecutionDrift, err)
	}
	current, err = repository.ParseTree(current)
	if err != nil {
		return PlatformPatchMergePlan{}, fmt.Errorf("%w: merge current tree: %v", ErrExecutionDrift, err)
	}
	patch, err = ParsePlatformPatch(patch)
	if err != nil {
		return PlatformPatchMergePlan{}, err
	}
	proposed, err := ApplyPlatformPatch(base, patch)
	if err != nil {
		return PlatformPatchMergePlan{}, err
	}

	plan := PlatformPatchMergePlan{
		BaseTreeHash: base.TreeHash, CurrentTreeHash: current.TreeHash,
		ProposedTreeHash: proposed.TreeHash, PlannedTreeHash: current.TreeHash,
		Operations: []repository.FileOperation{}, Conflicts: []PatchMergeConflict{},
	}
	baseFiles := indexTreeFiles(base)
	currentFiles := indexTreeFiles(current)
	proposedFiles := indexTreeFiles(proposed)
	operations := make([]repository.FileOperation, 0, len(patch.Operations))
	for index, patchOperation := range patch.Operations {
		path := patchOperation.Path
		baseState := patchState(baseFiles[path])
		currentState := patchState(currentFiles[path])
		proposedState := patchState(proposedFiles[path])
		switch {
		case currentState == proposedState:
			continue
		case currentState != baseState:
			plan.Conflicts = append(plan.Conflicts, PatchMergeConflict{
				Path: path, Reason: PatchMergeConflictConcurrentChange,
				Base: baseState, Current: currentState, Proposed: proposedState,
			})
			continue
		}

		operation := repository.FileOperation{
			ID:   fmt.Sprintf("agent-merge:%s:%04d", mergeID, index),
			Path: path,
		}
		if proposedState.Exists {
			operation.Kind = repository.OperationUpsert
			operation.ContentHash = proposedState.ContentHash
			operation.ByteSize = proposedState.ByteSize
			operation.Mode = proposedState.Mode
			if currentState.Exists {
				operation.ExpectedHash = currentState.ContentHash
			}
		} else {
			operation.Kind = repository.OperationDelete
			operation.ExpectedHash = currentState.ContentHash
		}
		operation, err = repository.NormalizeOperation(operation)
		if err != nil {
			return PlatformPatchMergePlan{}, fmt.Errorf("%w: derive merge operation: %v", ErrExecutionDrift, err)
		}
		operations = append(operations, operation)
	}
	if len(plan.Conflicts) != 0 {
		return plan, nil
	}

	planned := current
	for _, operation := range operations {
		planned, err = repository.ApplyOperation(planned, operation)
		if err != nil {
			return PlatformPatchMergePlan{}, fmt.Errorf("%w: apply merge plan: %v", ErrExecutionDrift, err)
		}
	}
	plan.Operations = operations
	plan.PlannedTreeHash = planned.TreeHash
	return plan, nil
}

func indexTreeFiles(tree repository.TreeManifest) map[string]*repository.TreeFile {
	files := make(map[string]*repository.TreeFile, len(tree.Files))
	for index := range tree.Files {
		file := tree.Files[index]
		files[file.Path] = &file
	}
	return files
}

func patchState(file *repository.TreeFile) PatchFileState {
	if file == nil {
		return PatchFileState{}
	}
	return PatchFileState{
		Exists: true, ContentHash: file.ContentHash, ByteSize: file.ByteSize, Mode: file.Mode,
	}
}
