package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

const MaxBatchMutationOperations = 2000

type ApplyBatchMutationInput struct {
	ProjectID                string          `json:"projectId"`
	CandidateID              string          `json:"candidateId"`
	ExpectedCandidateVersion uint64          `json:"expectedCandidateVersion"`
	ExpectedSessionEpoch     uint64          `json:"expectedSessionEpoch"`
	ExpectedWriterLeaseEpoch uint64          `json:"expectedWriterLeaseEpoch"`
	Operations               []FileOperation `json:"operations"`
}

type BatchMutationResult struct {
	Entries               []JournalEntry  `json:"entries"`
	BeforeTree            TreeBlobPointer `json:"beforeTree"`
	AfterTree             TreeBlobPointer `json:"afterTree"`
	Recovered             bool            `json:"recovered"`
	FinalizationPending   bool            `json:"finalizationPending"`
	FinalCandidateVersion uint64          `json:"finalCandidateVersion"`
}

func (service *MutationService) ApplyBatch(
	ctx context.Context,
	principal MutationPrincipal,
	input ApplyBatchMutationInput,
) (BatchMutationResult, error) {
	if service == nil || service.batchCandidates == nil {
		return BatchMutationResult{}, errors.New("repository atomic batch mutation store is unavailable")
	}
	input, err := normalizeBatchMutationInput(input)
	if err != nil {
		return BatchMutationResult{}, err
	}
	principal, err = normalizeMutationPrincipal(principal)
	if err != nil {
		return BatchMutationResult{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, principal.ActorID); err != nil {
		return BatchMutationResult{}, fmt.Errorf("authorize repository mutation batch: %w", err)
	}

	committed, foundCount, err := service.findCommittedBatch(ctx, principal, input)
	if err != nil {
		return BatchMutationResult{}, err
	}
	if foundCount != 0 {
		if foundCount != len(input.Operations) {
			return BatchMutationResult{}, errors.Join(
				ErrMutationReconciliation,
				fmt.Errorf("atomic repository batch has a partial committed identity (%d/%d)", foundCount, len(input.Operations)),
			)
		}
		return service.finalizeBatch(ctx, committed, true)
	}

	record, err := service.candidates.LoadMutationCandidate(ctx, input.ProjectID, input.CandidateID)
	if err != nil {
		return BatchMutationResult{}, fmt.Errorf("load repository Candidate for batch: %w", err)
	}
	firstInput := ApplyMutationInput{
		ProjectID: input.ProjectID, CandidateID: input.CandidateID,
		ExpectedCandidateVersion: input.ExpectedCandidateVersion,
		ExpectedSessionEpoch:     input.ExpectedSessionEpoch,
		ExpectedWriterLeaseEpoch: input.ExpectedWriterLeaseEpoch,
		Operation:                input.Operations[0],
	}
	if err := validateMutationRecord(record, firstInput); err != nil {
		return BatchMutationResult{}, err
	}
	currentTree, err := service.trees.Get(
		ctx, input.ProjectID, record.CurrentTreePointer.OwnerID, record.CurrentTreePointer,
	)
	if err != nil {
		return BatchMutationResult{}, fmt.Errorf("read authoritative Candidate tree for batch: %w", err)
	}
	if !equalTrees(currentTree, record.Candidate.CurrentTree) {
		return BatchMutationResult{}, ErrCandidateTreeDrift
	}

	subject := pathPolicySubject(record.Candidate)
	policy, err := service.policies.ResolvePathPolicy(ctx, subject)
	if err != nil {
		return BatchMutationResult{}, fmt.Errorf("resolve exact repository path policy for batch: %w", err)
	}
	policy, err = normalizePathPolicy(policy, subject)
	if err != nil {
		return BatchMutationResult{}, err
	}
	for _, operation := range input.Operations {
		if err := policy.authorize(principal.Attribution, operation); err != nil {
			return BatchMutationResult{}, err
		}
		if operation.Kind == OperationUpsert {
			if err := service.files.VerifyFileBlob(
				ctx, input.ProjectID, operation.ContentHash, operation.ByteSize,
			); err != nil {
				return BatchMutationResult{}, fmt.Errorf("verify repository batch file blob: %w", err)
			}
		}
	}

	commands := make([]AppendOperationCommand, 0, len(input.Operations))
	pending := make([]TreeBlobPointer, 0, len(input.Operations))
	candidate := record.Candidate
	beforePointer := record.CurrentTreePointer
	now := service.now().UTC()
	for _, operation := range input.Operations {
		next, entry, applyErr := candidate.Apply(
			candidate.Version,
			input.ExpectedSessionEpoch,
			input.ExpectedWriterLeaseEpoch,
			principal.ActorID,
			principal.Attribution,
			operation,
			now,
		)
		if applyErr != nil {
			service.abortBatchTrees(input.ProjectID, input.CandidateID, pending)
			return BatchMutationResult{}, applyErr
		}
		afterPointer, putErr := service.trees.PutPending(
			ctx, input.ProjectID, input.CandidateID, next.CurrentTree,
		)
		if putErr != nil {
			service.abortBatchTrees(input.ProjectID, input.CandidateID, pending)
			return BatchMutationResult{}, fmt.Errorf("put pending Candidate batch tree: %w", putErr)
		}
		if pointerErr := validateDerivedAfterPointer(afterPointer, input.CandidateID, next.CurrentTree); pointerErr != nil {
			service.abortBatchTrees(input.ProjectID, input.CandidateID, append(pending, afterPointer))
			return BatchMutationResult{}, pointerErr
		}
		commands = append(commands, AppendOperationCommand{
			ProjectID: input.ProjectID, CandidateAfter: next, Entry: entry,
			BeforeTree: beforePointer, AfterTree: afterPointer,
		})
		pending = append(pending, afterPointer)
		candidate = next
		beforePointer = afterPointer
	}

	committed, err = service.batchCandidates.AppendOperations(ctx, commands)
	if err != nil {
		result := BatchMutationResult{
			BeforeTree: record.CurrentTreePointer, AfterTree: pending[len(pending)-1],
			FinalCandidateVersion: candidate.Version,
		}
		if errors.Is(err, ErrAppendOutcomeUnknown) {
			result.FinalizationPending = true
			return result, errors.Join(ErrMutationReconciliation, err)
		}
		service.abortBatchTrees(input.ProjectID, input.CandidateID, pending)
		return BatchMutationResult{}, fmt.Errorf("append atomic repository mutation batch: %w", err)
	}
	if len(committed) != len(commands) {
		return BatchMutationResult{
			BeforeTree: record.CurrentTreePointer, AfterTree: pending[len(pending)-1],
			FinalizationPending: true, FinalCandidateVersion: candidate.Version,
		}, errors.Join(ErrMutationReconciliation, errors.New("atomic repository batch acknowledgement is incomplete"))
	}
	for index := range committed {
		if err := committed[index].matchesCommand(commands[index]); err != nil {
			return BatchMutationResult{
				BeforeTree: record.CurrentTreePointer, AfterTree: pending[len(pending)-1],
				FinalizationPending: true, FinalCandidateVersion: candidate.Version,
			}, errors.Join(ErrMutationReconciliation, err)
		}
	}
	return service.finalizeBatch(ctx, committed, false)
}

func (service *MutationService) findCommittedBatch(
	ctx context.Context,
	principal MutationPrincipal,
	input ApplyBatchMutationInput,
) ([]CommittedMutation, int, error) {
	committed := make([]CommittedMutation, len(input.Operations))
	foundCount := 0
	for index, operation := range input.Operations {
		value, found, err := service.candidates.FindCommittedOperation(
			ctx, input.ProjectID, input.CandidateID, operation.ID,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("find committed repository batch operation: %w", err)
		}
		if !found {
			continue
		}
		foundCount++
		retry := ApplyMutationInput{
			ProjectID: input.ProjectID, CandidateID: input.CandidateID,
			ExpectedCandidateVersion: input.ExpectedCandidateVersion + uint64(index),
			ExpectedSessionEpoch:     input.ExpectedSessionEpoch,
			ExpectedWriterLeaseEpoch: input.ExpectedWriterLeaseEpoch,
			Operation:                operation,
		}
		if err := value.matchesRetry(principal, retry); err != nil {
			return nil, 0, err
		}
		if err := service.verifyCommittedTransition(ctx, value); err != nil {
			return nil, 0, err
		}
		committed[index] = value
	}
	if foundCount == len(input.Operations) {
		for index := 1; index < len(committed); index++ {
			if committed[index].BeforeTree != committed[index-1].AfterTree ||
				committed[index].Entry.Sequence != committed[index-1].Entry.Sequence+1 ||
				committed[index].Entry.CandidateFrom != committed[index-1].Entry.CandidateTo {
				return nil, 0, ErrMutationReconciliation
			}
		}
	}
	return committed, foundCount, nil
}

func (service *MutationService) finalizeBatch(
	ctx context.Context,
	committed []CommittedMutation,
	recovered bool,
) (BatchMutationResult, error) {
	if len(committed) == 0 {
		return BatchMutationResult{}, ErrMutationStoreContract
	}
	result := BatchMutationResult{
		Entries:    make([]JournalEntry, len(committed)),
		BeforeTree: committed[0].BeforeTree, AfterTree: committed[len(committed)-1].AfterTree,
		Recovered: recovered, FinalCandidateVersion: committed[len(committed)-1].Entry.CandidateTo,
	}
	var finalizationErrs []error
	for index, value := range committed {
		result.Entries[index] = value.Entry
		if err := service.trees.Finalize(
			ctx, value.ProjectID, value.AfterTree.OwnerID, value.AfterTree,
		); err != nil {
			finalizationErrs = append(finalizationErrs, fmt.Errorf("finalize batch tree %d: %w", index, err))
		}
	}
	if len(finalizationErrs) != 0 {
		result.FinalizationPending = true
		return result, errors.Join(append([]error{ErrTreeFinalizationPending}, finalizationErrs...)...)
	}
	return result, nil
}

func (service *MutationService) abortBatchTrees(
	projectID, candidateID string,
	pointers []TreeBlobPointer,
) {
	for _, pointer := range pointers {
		_ = service.trees.Abort(context.Background(), projectID, candidateID, pointer)
	}
}

func normalizeBatchMutationInput(input ApplyBatchMutationInput) (ApplyBatchMutationInput, error) {
	if !validUUID(input.ProjectID) || !validUUID(input.CandidateID) ||
		input.ExpectedCandidateVersion == 0 || input.ExpectedSessionEpoch == 0 ||
		input.ExpectedWriterLeaseEpoch == 0 || len(input.Operations) == 0 ||
		len(input.Operations) > MaxBatchMutationOperations ||
		input.ExpectedCandidateVersion > ^uint64(0)-uint64(len(input.Operations)) {
		return ApplyBatchMutationInput{}, ErrInvalidMutation
	}
	operations := make([]FileOperation, len(input.Operations))
	seenIDs := make(map[string]bool, len(operations))
	seenPaths := make(map[string]bool, len(operations))
	for index, value := range input.Operations {
		operation, err := NormalizeOperation(value)
		if err != nil || operation != value ||
			(operation.Kind != OperationUpsert && operation.Kind != OperationDelete) ||
			seenIDs[operation.ID] || seenPaths[operation.Path] {
			return ApplyBatchMutationInput{}, ErrInvalidMutation
		}
		seenIDs[operation.ID] = true
		seenPaths[operation.Path] = true
		operations[index] = operation
	}
	if !sort.SliceIsSorted(operations, func(left, right int) bool {
		return operations[left].Path < operations[right].Path
	}) {
		return ApplyBatchMutationInput{}, ErrInvalidMutation
	}
	input.Operations = operations
	return input, nil
}

var _ BatchCandidateStore = (*GORMCandidateStore)(nil)
