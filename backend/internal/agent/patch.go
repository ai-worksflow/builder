package agent

import (
	"fmt"
	"sort"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	PlatformPatchSchemaVersion = "agent-platform-patch/v1"
	MaxPlatformPatchOperations = 2000
)

type PlatformPatch struct {
	SchemaVersion     string                     `json:"schemaVersion"`
	AttemptID         string                     `json:"attemptId"`
	ProjectID         string                     `json:"projectId"`
	CandidateID       string                     `json:"candidateId"`
	TaskCapsule       repository.ExactReference  `json:"taskCapsule"`
	ConfigurationHash string                     `json:"configurationHash"`
	BaseTreeHash      string                     `json:"baseTreeHash"`
	ProposedTreeHash  string                     `json:"proposedTreeHash"`
	Operations        []repository.FileOperation `json:"operations"`
	ChangedBytes      int64                      `json:"changedBytes"`
	ContentHash       string                     `json:"contentHash"`
}

func NewPlatformPatch(
	attempt AgentAttempt,
	capsule TaskCapsule,
	captured CapturedPatch,
) (PlatformPatch, error) {
	if attempt.SchemaVersion != AttemptSchemaVersion || !validUUIDs(attempt.ID, attempt.ProjectID, attempt.CandidateID) ||
		!sha256Pattern.MatchString(attempt.ConfigurationHash) ||
		attempt.ProjectID != capsule.ProjectID || attempt.CandidateID != capsule.CandidateID ||
		attempt.TaskCapsule != capsule.ExactReference() ||
		attempt.BaseCandidateTreeHash != capsule.BaseCandidateTreeHash ||
		captured.BaseTree.TreeHash != capsule.BaseCandidateTreeHash ||
		captured.ProposedTree.TreeHash == captured.BaseTree.TreeHash || len(captured.Changes) == 0 ||
		len(captured.Changes) > MaxPlatformPatchOperations ||
		captured.ChangedBytes < 0 || captured.ChangedBytes > capsule.Budgets.MaxPatchBytes {
		return PlatformPatch{}, fmt.Errorf("%w: captured patch binding", ErrExecutionDrift)
	}
	operations := make([]repository.FileOperation, len(captured.Changes))
	for index, change := range captured.Changes {
		operation, err := repository.NormalizeOperation(change.Operation)
		if err != nil {
			return PlatformPatch{}, fmt.Errorf("%w: operation %d: %v", ErrExecutionDrift, index, err)
		}
		if pathInPolicySet(operation.Path, capsule.ProtectedPaths) ||
			!pathInPolicySet(operation.Path, capsule.WriteSet) ||
			(operation.FromPath != "" && (pathInPolicySet(operation.FromPath, capsule.ProtectedPaths) ||
				!pathInPolicySet(operation.FromPath, capsule.WriteSet))) {
			return PlatformPatch{}, fmt.Errorf("%w: operation %d path", ErrPatchPolicy, index)
		}
		operations[index] = operation
	}
	if !sort.SliceIsSorted(operations, func(left, right int) bool {
		return operations[left].Path < operations[right].Path
	}) {
		return PlatformPatch{}, fmt.Errorf("%w: operations are not in canonical path order", ErrExecutionDrift)
	}
	projected := captured.BaseTree
	for _, operation := range operations {
		var err error
		projected, err = repository.ApplyOperation(projected, operation)
		if err != nil {
			return PlatformPatch{}, fmt.Errorf("%w: apply operation: %v", ErrExecutionDrift, err)
		}
	}
	if projected.TreeHash != captured.ProposedTree.TreeHash {
		return PlatformPatch{}, fmt.Errorf("%w: proposed tree hash", ErrExecutionDrift)
	}
	patch := PlatformPatch{
		SchemaVersion: PlatformPatchSchemaVersion,
		AttemptID:     attempt.ID, ProjectID: attempt.ProjectID, CandidateID: attempt.CandidateID,
		TaskCapsule: attempt.TaskCapsule, ConfigurationHash: attempt.ConfigurationHash,
		BaseTreeHash: captured.BaseTree.TreeHash, ProposedTreeHash: captured.ProposedTree.TreeHash,
		Operations: operations, ChangedBytes: captured.ChangedBytes,
	}
	hash, err := domain.CanonicalHash(platformPatchPayload(patch))
	if err != nil {
		return PlatformPatch{}, fmt.Errorf("%w: hash platform patch: %v", ErrExecutionDrift, err)
	}
	patch.ContentHash = "sha256:" + hash
	return patch, nil
}

func ParsePlatformPatch(patch PlatformPatch) (PlatformPatch, error) {
	if patch.SchemaVersion != PlatformPatchSchemaVersion || !validUUIDs(
		patch.AttemptID, patch.ProjectID, patch.CandidateID, patch.TaskCapsule.ID,
	) || !sha256Pattern.MatchString(patch.TaskCapsule.ContentHash) ||
		!sha256Pattern.MatchString(patch.ConfigurationHash) ||
		!sha256Pattern.MatchString(patch.BaseTreeHash) ||
		!sha256Pattern.MatchString(patch.ProposedTreeHash) ||
		patch.BaseTreeHash == patch.ProposedTreeHash || !sha256Pattern.MatchString(patch.ContentHash) ||
		patch.ChangedBytes < 0 || len(patch.Operations) == 0 || len(patch.Operations) > MaxPlatformPatchOperations {
		return PlatformPatch{}, fmt.Errorf("%w: platform patch identity", ErrExecutionDrift)
	}
	operations := make([]repository.FileOperation, len(patch.Operations))
	seen := make(map[string]bool, len(operations))
	changedBytes := int64(0)
	for index, value := range patch.Operations {
		operation, err := repository.NormalizeOperation(value)
		if err != nil || operation != value || seen[operation.Path] ||
			(operation.Kind != repository.OperationUpsert && operation.Kind != repository.OperationDelete) {
			return PlatformPatch{}, fmt.Errorf("%w: non-canonical or duplicate patch operation", ErrExecutionDrift)
		}
		if operation.Kind == repository.OperationUpsert {
			changedBytes += operation.ByteSize
			if changedBytes > repository.MaxTreeBytes {
				return PlatformPatch{}, fmt.Errorf("%w: patch byte count", ErrExecutionDrift)
			}
		}
		seen[operation.Path] = true
		operations[index] = operation
	}
	if changedBytes != patch.ChangedBytes {
		return PlatformPatch{}, fmt.Errorf("%w: patch changed byte count", ErrExecutionDrift)
	}
	if !sort.SliceIsSorted(operations, func(left, right int) bool {
		return operations[left].Path < operations[right].Path
	}) {
		return PlatformPatch{}, fmt.Errorf("%w: patch operation order", ErrExecutionDrift)
	}
	expected, err := domain.CanonicalHash(platformPatchPayload(patch))
	if err != nil {
		return PlatformPatch{}, err
	}
	if patch.ContentHash != "sha256:"+expected {
		return PlatformPatch{}, fmt.Errorf("%w: platform patch hash", ErrExecutionDrift)
	}
	patch.Operations = operations
	return patch, nil
}

func ApplyPlatformPatch(base repository.TreeManifest, patch PlatformPatch) (repository.TreeManifest, error) {
	patch, err := ParsePlatformPatch(patch)
	if err != nil {
		return repository.TreeManifest{}, err
	}
	base, err = repository.ParseTree(base)
	if err != nil || base.TreeHash != patch.BaseTreeHash {
		return repository.TreeManifest{}, fmt.Errorf("%w: patch base tree", ErrExecutionDrift)
	}
	for _, operation := range patch.Operations {
		base, err = repository.ApplyOperation(base, operation)
		if err != nil {
			return repository.TreeManifest{}, err
		}
	}
	if base.TreeHash != patch.ProposedTreeHash {
		return repository.TreeManifest{}, fmt.Errorf("%w: patch result tree", ErrExecutionDrift)
	}
	return base, nil
}

func platformPatchPayload(patch PlatformPatch) any {
	return struct {
		SchemaVersion     string                     `json:"schemaVersion"`
		AttemptID         string                     `json:"attemptId"`
		ProjectID         string                     `json:"projectId"`
		CandidateID       string                     `json:"candidateId"`
		TaskCapsule       repository.ExactReference  `json:"taskCapsule"`
		ConfigurationHash string                     `json:"configurationHash"`
		BaseTreeHash      string                     `json:"baseTreeHash"`
		ProposedTreeHash  string                     `json:"proposedTreeHash"`
		Operations        []repository.FileOperation `json:"operations"`
		ChangedBytes      int64                      `json:"changedBytes"`
	}{
		SchemaVersion: patch.SchemaVersion, AttemptID: patch.AttemptID,
		ProjectID: patch.ProjectID, CandidateID: patch.CandidateID,
		TaskCapsule: patch.TaskCapsule, ConfigurationHash: patch.ConfigurationHash,
		BaseTreeHash: patch.BaseTreeHash, ProposedTreeHash: patch.ProposedTreeHash,
		Operations: patch.Operations, ChangedBytes: patch.ChangedBytes,
	}
}
