package agent

import (
	"fmt"

	"github.com/worksflow/builder/backend/internal/repository"
)

// PlatformPatchUndoPlan reverses only the paths changed by one exact applied
// merge. Unrelated Candidate changes are preserved. As with merge planning,
// any affected-path conflict suppresses the entire operation batch.
type PlatformPatchUndoPlan struct {
	MergeID             string                     `json:"mergeId"`
	MergeBeforeTreeHash string                     `json:"mergeBeforeTreeHash"`
	MergedTreeHash      string                     `json:"mergedTreeHash"`
	CurrentTreeHash     string                     `json:"currentTreeHash"`
	PlannedTreeHash     string                     `json:"plannedTreeHash"`
	Operations          []repository.FileOperation `json:"operations"`
	Conflicts           []PatchMergeConflict       `json:"conflicts"`
}

func PlanPlatformPatchMergeUndo(
	mergeBefore repository.TreeManifest,
	current repository.TreeManifest,
	merge PatchMergePlanRecord,
	undoID string,
) (PlatformPatchUndoPlan, error) {
	if !validUUIDs(undoID) {
		return PlatformPatchUndoPlan{}, fmt.Errorf("%w: undo identity", ErrPatchMergeInvalid)
	}
	merge, err := ParsePatchMergePlanRecord(merge)
	if err != nil || merge.Disposition != PatchMergePlanned {
		return PlatformPatchUndoPlan{}, fmt.Errorf("%w: undo merge source", ErrPatchMergeInvalid)
	}
	mergeBefore, err = repository.ParseTree(mergeBefore)
	if err != nil || mergeBefore.TreeHash != merge.CurrentTreeHash {
		return PlatformPatchUndoPlan{}, fmt.Errorf("%w: undo before tree", ErrPatchMergeInvalid)
	}
	current, err = repository.ParseTree(current)
	if err != nil {
		return PlatformPatchUndoPlan{}, fmt.Errorf("%w: undo current tree", ErrPatchMergeInvalid)
	}
	merged := mergeBefore
	for _, operation := range merge.Operations {
		merged, err = repository.ApplyOperation(merged, operation)
		if err != nil {
			return PlatformPatchUndoPlan{}, fmt.Errorf("%w: reconstruct merged tree: %v", ErrPatchMergeInvalid, err)
		}
	}
	if merged.TreeHash != merge.PlannedTreeHash {
		return PlatformPatchUndoPlan{}, fmt.Errorf("%w: reconstructed merged tree hash", ErrPatchMergeInvalid)
	}
	plan := PlatformPatchUndoPlan{
		MergeID: merge.ID, MergeBeforeTreeHash: mergeBefore.TreeHash,
		MergedTreeHash: merged.TreeHash, CurrentTreeHash: current.TreeHash,
		PlannedTreeHash: current.TreeHash, Operations: []repository.FileOperation{},
		Conflicts: []PatchMergeConflict{},
	}
	beforeFiles := indexTreeFiles(mergeBefore)
	mergedFiles := indexTreeFiles(merged)
	currentFiles := indexTreeFiles(current)
	operations := make([]repository.FileOperation, 0, len(merge.Operations))
	for index, mergedOperation := range merge.Operations {
		path := mergedOperation.Path
		beforeState := patchState(beforeFiles[path])
		mergedState := patchState(mergedFiles[path])
		currentState := patchState(currentFiles[path])
		switch {
		case currentState == beforeState:
			continue
		case currentState != mergedState:
			plan.Conflicts = append(plan.Conflicts, PatchMergeConflict{
				Path: path, Reason: PatchMergeConflictConcurrentChange,
				Base: mergedState, Current: currentState, Proposed: beforeState,
			})
			continue
		}
		operation := repository.FileOperation{
			ID: fmt.Sprintf("agent-undo:%s:%04d", undoID, index), Path: path,
		}
		if beforeState.Exists {
			operation.Kind = repository.OperationUpsert
			operation.ContentHash = beforeState.ContentHash
			operation.ByteSize = beforeState.ByteSize
			operation.Mode = beforeState.Mode
			if currentState.Exists {
				operation.ExpectedHash = currentState.ContentHash
			}
		} else {
			operation.Kind = repository.OperationDelete
			operation.ExpectedHash = currentState.ContentHash
		}
		operation, err = repository.NormalizeOperation(operation)
		if err != nil {
			return PlatformPatchUndoPlan{}, fmt.Errorf("%w: derive undo operation: %v", ErrPatchMergeInvalid, err)
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
			return PlatformPatchUndoPlan{}, fmt.Errorf("%w: apply undo plan: %v", ErrPatchMergeInvalid, err)
		}
	}
	plan.Operations = operations
	plan.PlannedTreeHash = planned.TreeHash
	return plan, nil
}
