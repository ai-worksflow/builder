package repository

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const candidateRebaseLeaseTTL = 5 * time.Minute

var ErrCandidateRebaseReconciliation = errors.New("repository Candidate rebase requires reconciliation")

type StartCandidateRebaseInput struct {
	ProjectID                string `json:"projectId"`
	PredecessorCandidateID   string `json:"predecessorCandidateId"`
	TargetBuildManifestID    string `json:"targetBuildManifestId"`
	ExpectedCandidateVersion uint64 `json:"expectedCandidateVersion"`
	ExpectedSessionEpoch     uint64 `json:"expectedSessionEpoch"`
	ExpectedWriterLeaseEpoch uint64 `json:"expectedWriterLeaseEpoch"`
	ActorID                  string `json:"-"`
	OperationID              string `json:"-"`
}

type ResolveCandidateRebaseConflictInput struct {
	ProjectID               string
	RebaseID                string
	ConflictID              string
	ExpectedConflictVersion uint64
	ActorID                 string
	Strategy                CandidateRebaseResolutionStrategy
	Content                 *string
	Mode                    string
}

type CandidateRebaseResult struct {
	Rebase              CandidateRebase    `json:"rebase"`
	Candidate           CandidateWorkspace `json:"candidate"`
	Created             bool               `json:"created"`
	Recovered           bool               `json:"recovered"`
	FinalizationPending bool               `json:"finalizationPending"`
}

type CandidateRebaseFileContent struct {
	ContentHash string `json:"contentHash"`
	ByteSize    int64  `json:"byteSize"`
	Encoding    string `json:"encoding"`
	Data        string `json:"data"`
}

type CandidateRebaseConflictContent struct {
	SchemaVersion string                      `json:"schemaVersion"`
	RebaseID      string                      `json:"rebaseId"`
	ConflictID    string                      `json:"conflictId"`
	Path          string                      `json:"path"`
	Ancestor      *CandidateRebaseFileContent `json:"ancestor,omitempty"`
	Predecessor   *CandidateRebaseFileContent `json:"predecessor,omitempty"`
	Target        *CandidateRebaseFileContent `json:"target,omitempty"`
}

type CandidateRebaseFileStore interface {
	BootstrapFileStore
}

type CandidateRebaseService struct {
	bootstrap  *CandidateBootstrapService
	store      *CandidateRebaseStore
	controls   *CandidateControlStore
	candidates *GORMCandidateStore
	mutations  *MutationService
	files      CandidateRebaseFileStore
	access     MutationAuthorizer
	now        func() time.Time
}

func NewCandidateRebaseService(
	bootstrap *CandidateBootstrapService,
	store *CandidateRebaseStore,
	controls *CandidateControlStore,
	candidates *GORMCandidateStore,
	mutations *MutationService,
	files CandidateRebaseFileStore,
	access MutationAuthorizer,
	now func() time.Time,
) (*CandidateRebaseService, error) {
	if bootstrap == nil || store == nil || controls == nil || candidates == nil || mutations == nil ||
		files == nil || access == nil || now == nil {
		return nil, errors.New("Candidate rebase services, stores, authorizer, and clock are required")
	}
	return &CandidateRebaseService{
		bootstrap: bootstrap, store: store, controls: controls, candidates: candidates,
		mutations: mutations, files: files, access: access, now: now,
	}, nil
}

func (service *CandidateRebaseService) Start(
	ctx context.Context,
	input StartCandidateRebaseInput,
) (CandidateRebaseResult, error) {
	input, err := normalizeStartCandidateRebaseInput(input)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return CandidateRebaseResult{}, fmt.Errorf("authorize Candidate rebase: %w", err)
	}
	rebaseID := candidateRebaseID(input)
	if existing, found, findErr := service.store.FindByOperation(
		ctx, input.ProjectID, input.ActorID, input.OperationID,
	); findErr != nil {
		return CandidateRebaseResult{}, findErr
	} else if found {
		if err := existingCandidateRebaseMatches(existing, rebaseID, input); err != nil {
			return CandidateRebaseResult{}, err
		}
		return service.resume(ctx, existing, input.ActorID, false, true)
	}

	predecessor, err := service.candidates.LoadMutationCandidate(
		ctx, input.ProjectID, input.PredecessorCandidateID,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CandidateRebaseResult{}, ErrCandidateNotFound
	}
	if err != nil {
		return CandidateRebaseResult{}, fmt.Errorf("load rebase predecessor Candidate: %w", err)
	}
	if err := validateRebasePredecessor(predecessor.Candidate, input); err != nil {
		return CandidateRebaseResult{}, err
	}
	ancestor, err := service.store.LoadBaseTree(ctx, predecessor.Candidate)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	bootstrapped, err := service.bootstrap.Bootstrap(ctx, BootstrapCandidateInput{
		ProjectID: input.ProjectID, BuildManifestID: input.TargetBuildManifestID,
		ActorID: input.ActorID, OperationID: "rebase:" + rebaseID,
	})
	if err != nil {
		return CandidateRebaseResult{}, fmt.Errorf("bootstrap exact rebase target Candidate: %w", err)
	}
	successor := bootstrapped.Candidate
	if successor.ProjectID != input.ProjectID || successor.BuildManifest.ID != input.TargetBuildManifestID ||
		successor.ID == predecessor.Candidate.ID || successor.Status != CandidateActive || successor.Dirty ||
		successor.Conflicted || successor.Stale || successor.RebaseRequired || successor.JournalSequence != 0 ||
		successor.CurrentTree.TreeHash != successor.BaseTreeHash {
		return CandidateRebaseResult{}, fmt.Errorf("%w: bootstrapped successor is not the exact clean target", ErrCandidateRebaseState)
	}
	plan, err := PlanCandidateRebase(rebaseID, ancestor, predecessor.Candidate.CurrentTree, successor.CurrentTree)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	createdAt := service.now().UTC().Truncate(time.Microsecond)
	if createdAt.IsZero() {
		return CandidateRebaseResult{}, ErrInvalidRebase
	}
	rebase := CandidateRebase{
		SchemaVersion: CandidateRebaseSchemaVersion, ID: rebaseID, ProjectID: input.ProjectID,
		OperationID: input.OperationID, PredecessorCandidateID: input.PredecessorCandidateID,
		SuccessorCandidateID: successor.ID, TargetBuildManifestID: input.TargetBuildManifestID,
		AncestorTreeHash: plan.AncestorTreeHash, PredecessorTreeHash: plan.PredecessorTreeHash,
		TargetTreeHash: plan.TargetTreeHash, PlannedTreeHash: plan.PlannedTreeHash, PlanHash: plan.PlanHash,
		State: CandidateRebaseApplying, Version: 1, CreatedBy: input.ActorID,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	created, err := service.store.Create(ctx, CreateCandidateRebaseCommand{
		Rebase: rebase, Plan: plan, ExpectedCandidateVersion: input.ExpectedCandidateVersion,
		ExpectedSessionEpoch:     input.ExpectedSessionEpoch,
		ExpectedWriterLeaseEpoch: input.ExpectedWriterLeaseEpoch,
	})
	if err != nil {
		if errors.Is(err, ErrCandidateRebaseReplay) {
			existing, found, findErr := service.store.FindByOperation(
				ctx, input.ProjectID, input.ActorID, input.OperationID,
			)
			if findErr == nil && found && existingCandidateRebaseMatches(existing, rebaseID, input) == nil {
				return service.resume(ctx, existing, input.ActorID, false, true)
			}
			if findErr != nil {
				return CandidateRebaseResult{}, errors.Join(err, findErr)
			}
		}
		return CandidateRebaseResult{}, err
	}
	return service.resume(ctx, created, input.ActorID, true, bootstrapped.Recovered)
}

func (service *CandidateRebaseService) Get(
	ctx context.Context,
	projectID, rebaseID, actorID string,
) (CandidateRebaseResult, error) {
	projectID = strings.TrimSpace(projectID)
	rebaseID = strings.TrimSpace(rebaseID)
	actorID = strings.TrimSpace(actorID)
	if !validUUID(projectID) || !validUUID(rebaseID) || !validUUID(actorID) {
		return CandidateRebaseResult{}, ErrInvalidRebase
	}
	if err := service.access.RequireProjectEdit(ctx, projectID, actorID); err != nil {
		return CandidateRebaseResult{}, fmt.Errorf("authorize Candidate rebase read: %w", err)
	}
	rebase, err := service.store.Get(ctx, projectID, rebaseID)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	candidate, err := service.loadSuccessor(ctx, rebase)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	return CandidateRebaseResult{Rebase: rebase, Candidate: candidate}, nil
}

func (service *CandidateRebaseService) ResolveConflict(
	ctx context.Context,
	input ResolveCandidateRebaseConflictInput,
) (CandidateRebaseResult, error) {
	input, err := normalizeResolveCandidateRebaseConflictInput(input)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return CandidateRebaseResult{}, fmt.Errorf("authorize Candidate rebase conflict resolution: %w", err)
	}
	rebase, err := service.store.Get(ctx, input.ProjectID, input.RebaseID)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	conflict, found := findCandidateRebaseConflict(rebase, input.ConflictID)
	if !found {
		return CandidateRebaseResult{}, ErrCandidateRebaseConflictNotFound
	}
	selected, finalizationPending, err := service.resolveSelectedFile(ctx, input, conflict)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	if conflict.State == CandidateRebaseConflictResolved {
		if conflictResolutionMatches(conflict, input.Strategy, selected) {
			candidate, loadErr := service.loadSuccessor(ctx, rebase)
			return CandidateRebaseResult{
				Rebase: rebase, Candidate: candidate, Recovered: true,
				FinalizationPending: finalizationPending,
			}, loadErr
		}
		return CandidateRebaseResult{}, ErrCandidateRebaseReplay
	}
	if rebase.State != CandidateRebaseConflicted || conflict.Version != input.ExpectedConflictVersion {
		return CandidateRebaseResult{}, ErrCandidateRebaseState
	}

	candidate, err := service.loadSuccessor(ctx, rebase)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	if !candidate.Conflicted || candidate.Stale || candidate.RebaseRequired || candidate.Status != CandidateActive {
		return CandidateRebaseResult{}, ErrCandidateRebaseState
	}
	current := treeFileAt(treeFilesByPath(candidate.CurrentTree), conflict.Path)
	if !equalTreeFile(current, selected) {
		if !equalTreeFile(current, conflict.TargetFile) {
			return CandidateRebaseResult{}, fmt.Errorf("%w: conflict path changed outside its exact resolution", ErrCandidateRebaseState)
		}
		candidate, err = service.ensureMergeLease(ctx, candidate, input.ActorID)
		if err != nil {
			return CandidateRebaseResult{}, err
		}
		operation := rebaseFileOperation(input.RebaseID, conflict.Ordinal, conflict.Path, current, selected)
		operation.ID = "rebase-resolve:" + conflict.ID
		result, applyErr := service.mutations.Apply(ctx, MutationPrincipal{
			ActorID: input.ActorID, Attribution: "merge",
		}, ApplyMutationInput{
			ProjectID: input.ProjectID, CandidateID: candidate.ID,
			ExpectedCandidateVersion: candidate.Version, ExpectedSessionEpoch: candidate.SessionEpoch,
			ExpectedWriterLeaseEpoch: candidate.WriterLeaseEpoch, Operation: operation,
		})
		if applyErr != nil {
			if result.Entry.Operation.ID != "" {
				return CandidateRebaseResult{
					Rebase: rebase, Candidate: candidate, FinalizationPending: result.FinalizationPending,
				}, errors.Join(ErrCandidateRebaseReconciliation, applyErr)
			}
			return CandidateRebaseResult{}, applyErr
		}
		finalizationPending = finalizationPending || result.FinalizationPending
		candidate, err = service.loadSuccessor(ctx, rebase)
		if err != nil {
			return CandidateRebaseResult{}, err
		}
	}
	actual := treeFileAt(treeFilesByPath(candidate.CurrentTree), conflict.Path)
	if !equalTreeFile(actual, selected) {
		return CandidateRebaseResult{}, rebaseStoreContract("resolved Candidate tree differs from selected file", nil)
	}

	resolved, err := service.store.ResolveConflict(ctx, ResolveCandidateRebaseConflictCommand{
		ProjectID: input.ProjectID, RebaseID: input.RebaseID, ConflictID: input.ConflictID,
		ExpectedConflictVersion:           input.ExpectedConflictVersion,
		ExpectedSuccessorCandidateVersion: candidate.Version, ExpectedSessionEpoch: candidate.SessionEpoch,
		ExpectedWriterLeaseEpoch: candidate.WriterLeaseEpoch, ActorID: input.ActorID,
		Strategy: input.Strategy, ResolutionFile: cloneTreeFile(selected),
	})
	if err != nil {
		currentRebase, loadErr := service.store.Get(ctx, input.ProjectID, input.RebaseID)
		if loadErr == nil {
			currentConflict, exists := findCandidateRebaseConflict(currentRebase, input.ConflictID)
			if exists && conflictResolutionMatches(currentConflict, input.Strategy, selected) {
				currentCandidate, candidateErr := service.loadSuccessor(ctx, currentRebase)
				return CandidateRebaseResult{
					Rebase: currentRebase, Candidate: currentCandidate, Recovered: true,
					FinalizationPending: finalizationPending,
				}, candidateErr
			}
		}
		return CandidateRebaseResult{}, errors.Join(ErrCandidateRebaseReconciliation, err, loadErr)
	}
	candidate, err = service.loadSuccessor(ctx, resolved)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	return CandidateRebaseResult{
		Rebase: resolved, Candidate: candidate, FinalizationPending: finalizationPending,
	}, nil
}

func (service *CandidateRebaseService) ReadConflictContent(
	ctx context.Context,
	projectID, rebaseID, conflictID, actorID string,
) (CandidateRebaseConflictContent, error) {
	projectID = strings.TrimSpace(projectID)
	rebaseID = strings.TrimSpace(rebaseID)
	conflictID = strings.TrimSpace(conflictID)
	actorID = strings.TrimSpace(actorID)
	if !validUUID(projectID) || !validUUID(rebaseID) || !validUUID(conflictID) || !validUUID(actorID) {
		return CandidateRebaseConflictContent{}, ErrInvalidRebase
	}
	if err := service.access.RequireProjectEdit(ctx, projectID, actorID); err != nil {
		return CandidateRebaseConflictContent{}, fmt.Errorf("authorize Candidate rebase conflict content: %w", err)
	}
	rebase, err := service.store.Get(ctx, projectID, rebaseID)
	if err != nil {
		return CandidateRebaseConflictContent{}, err
	}
	conflict, found := findCandidateRebaseConflict(rebase, conflictID)
	if !found {
		return CandidateRebaseConflictContent{}, ErrCandidateRebaseConflictNotFound
	}
	ancestor, err := service.readConflictFile(ctx, projectID, conflict.AncestorFile)
	if err != nil {
		return CandidateRebaseConflictContent{}, err
	}
	predecessor, err := service.readConflictFile(ctx, projectID, conflict.PredecessorFile)
	if err != nil {
		return CandidateRebaseConflictContent{}, err
	}
	target, err := service.readConflictFile(ctx, projectID, conflict.TargetFile)
	if err != nil {
		return CandidateRebaseConflictContent{}, err
	}
	return CandidateRebaseConflictContent{
		SchemaVersion: "candidate-rebase-conflict-content/v1", RebaseID: rebase.ID,
		ConflictID: conflict.ID, Path: conflict.Path,
		Ancestor: ancestor, Predecessor: predecessor, Target: target,
	}, nil
}

func (service *CandidateRebaseService) resume(
	ctx context.Context,
	rebase CandidateRebase,
	actorID string,
	created, recovered bool,
) (CandidateRebaseResult, error) {
	if rebase.State != CandidateRebaseApplying {
		candidate, err := service.loadSuccessor(ctx, rebase)
		return CandidateRebaseResult{
			Rebase: rebase, Candidate: candidate, Created: created, Recovered: recovered,
		}, err
	}
	for _, planned := range rebase.Operations {
		committed, found, err := service.candidates.FindCommittedOperation(
			ctx, rebase.ProjectID, rebase.SuccessorCandidateID, planned.Operation.ID,
		)
		if err != nil {
			return CandidateRebaseResult{}, err
		}
		if found {
			if committed.ProjectID != rebase.ProjectID || committed.Entry.CandidateID != rebase.SuccessorCandidateID ||
				committed.Entry.ActorID != actorID || committed.Entry.Attribution != "merge" ||
				committed.Entry.Operation != planned.Operation {
				return CandidateRebaseResult{}, ErrCandidateRebaseReplay
			}
			if err := service.mutations.verifyCommittedTransition(ctx, committed); err != nil {
				return CandidateRebaseResult{}, err
			}
			if _, err := service.mutations.finalize(ctx, committed, true); err != nil {
				return CandidateRebaseResult{}, errors.Join(ErrCandidateRebaseReconciliation, err)
			}
			recovered = true
			continue
		}
		candidate, err := service.loadSuccessor(ctx, rebase)
		if err != nil {
			return CandidateRebaseResult{}, err
		}
		if candidate.Conflicted || candidate.Stale || candidate.RebaseRequired || candidate.Status != CandidateActive {
			return CandidateRebaseResult{}, ErrCandidateRebaseState
		}
		candidate, err = service.ensureMergeLease(ctx, candidate, actorID)
		if err != nil {
			return CandidateRebaseResult{}, err
		}
		result, err := service.mutations.Apply(ctx, MutationPrincipal{
			ActorID: actorID, Attribution: "merge",
		}, ApplyMutationInput{
			ProjectID: rebase.ProjectID, CandidateID: rebase.SuccessorCandidateID,
			ExpectedCandidateVersion: candidate.Version, ExpectedSessionEpoch: candidate.SessionEpoch,
			ExpectedWriterLeaseEpoch: candidate.WriterLeaseEpoch, Operation: planned.Operation,
		})
		if err != nil {
			return CandidateRebaseResult{
				Rebase: rebase, Candidate: candidate, Created: created, Recovered: recovered,
				FinalizationPending: result.FinalizationPending,
			}, errors.Join(ErrCandidateRebaseReconciliation, err)
		}
	}
	candidate, err := service.loadSuccessor(ctx, rebase)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	if candidate.CurrentTree.TreeHash != rebase.PlannedTreeHash {
		return CandidateRebaseResult{}, rebaseStoreContract("automatic rebase result differs from planned tree", nil)
	}
	targetState := CandidateRebaseReady
	if len(rebase.Conflicts) != 0 {
		targetState = CandidateRebaseConflicted
		if !candidate.Conflicted {
			updated, updateErr := service.controls.UpdateFlags(ctx, UpdateCandidateFlagsInput{
				ProjectID: rebase.ProjectID, CandidateID: candidate.ID,
				ExpectedCandidateVersion: candidate.Version, ExpectedSessionEpoch: candidate.SessionEpoch,
				ExpectedWriterLeaseEpoch: candidate.WriterLeaseEpoch, ActorID: actorID,
				Flags:       CandidateFlags{Conflicted: true},
				Reason:      "exact three-way rebase contains unresolved file conflicts",
				EvidenceRef: candidateRebaseEvidenceRef(rebase.ID), EvidenceHash: rebase.PlanHash,
			})
			if updateErr != nil {
				return CandidateRebaseResult{}, updateErr
			}
			candidate = updated.Candidate
		} else {
			matches, evidenceErr := service.store.FlagEvidenceMatches(
				ctx, rebase.ProjectID, candidate.ID, candidate.Version, true, rebase.ID, rebase.PlanHash,
			)
			if evidenceErr != nil || !matches {
				return CandidateRebaseResult{}, errors.Join(
					ErrCandidateRebaseReconciliation, evidenceErr,
					fmt.Errorf("successor conflict flag is not bound to this exact rebase plan"),
				)
			}
		}
	} else if candidate.Conflicted || candidate.Stale || candidate.RebaseRequired {
		return CandidateRebaseResult{}, ErrCandidateRebaseState
	}
	transitioned, err := service.store.Transition(
		ctx, rebase.ProjectID, rebase.ID, CandidateRebaseApplying, targetState, rebase.Version,
	)
	if err != nil {
		current, loadErr := service.store.Get(ctx, rebase.ProjectID, rebase.ID)
		if loadErr == nil && current.State == targetState {
			transitioned = current
			recovered = true
		} else {
			return CandidateRebaseResult{}, errors.Join(ErrCandidateRebaseReconciliation, err, loadErr)
		}
	}
	candidate, err = service.loadSuccessor(ctx, transitioned)
	if err != nil {
		return CandidateRebaseResult{}, err
	}
	return CandidateRebaseResult{
		Rebase: transitioned, Candidate: candidate, Created: created, Recovered: recovered,
	}, nil
}

func (service *CandidateRebaseService) ensureMergeLease(
	ctx context.Context,
	candidate CandidateWorkspace,
	actorID string,
) (CandidateWorkspace, error) {
	now := service.now().UTC()
	if candidate.Lease != nil && candidate.Lease.OwnerID == actorID && now.Before(candidate.Lease.ExpiresAt) {
		return candidate, nil
	}
	leased, err := service.controls.AcquireLease(
		ctx, candidate.ProjectID, candidate.ID, candidate.Version, actorID, candidateRebaseLeaseTTL,
	)
	if err != nil {
		return CandidateWorkspace{}, err
	}
	return leased.Candidate, nil
}

func (service *CandidateRebaseService) resolveSelectedFile(
	ctx context.Context,
	input ResolveCandidateRebaseConflictInput,
	conflict CandidateRebaseConflictRecord,
) (*TreeFile, bool, error) {
	switch input.Strategy {
	case CandidateRebaseUsePredecessor:
		if input.Content != nil || input.Mode != "" {
			return nil, false, ErrInvalidRebase
		}
		return cloneTreeFile(conflict.PredecessorFile), false, nil
	case CandidateRebaseUseTarget:
		if input.Content != nil || input.Mode != "" {
			return nil, false, ErrInvalidRebase
		}
		return cloneTreeFile(conflict.TargetFile), false, nil
	case CandidateRebaseUseCurrent:
		if input.Content == nil {
			return nil, false, ErrInvalidRebase
		}
		value := []byte(*input.Content)
		if int64(len(value)) > MaxFileBytes {
			return nil, false, ErrTreeLimit
		}
		written, err := service.files.Put(ctx, input.ProjectID, input.ActorID, value)
		if err != nil && !errors.Is(err, ErrFileBlobFinalizationPending) {
			return nil, false, err
		}
		mode := input.Mode
		if mode == "" {
			switch {
			case conflict.TargetFile != nil:
				mode = conflict.TargetFile.Mode
			case conflict.PredecessorFile != nil:
				mode = conflict.PredecessorFile.Mode
			default:
				mode = "100644"
			}
		}
		file, normalizeErr := normalizeTreeFile(TreeFile{
			Path: conflict.Path, Mode: mode, ContentHash: written.Pointer.ContentHash,
			ByteSize: written.Pointer.ByteSize,
		})
		if normalizeErr != nil || file.ContentHash != rawFileContentHash(value) || file.ByteSize != int64(len(value)) {
			return nil, false, rebaseStoreContract("custom resolution blob differs from submitted bytes", normalizeErr)
		}
		return &file, written.FinalizationPending || err != nil, nil
	default:
		return nil, false, ErrInvalidRebase
	}
}

func (service *CandidateRebaseService) readConflictFile(
	ctx context.Context,
	projectID string,
	file *TreeFile,
) (*CandidateRebaseFileContent, error) {
	if file == nil {
		return nil, nil
	}
	pointer, value, err := service.files.Resolve(ctx, projectID, file.ContentHash, file.ByteSize)
	if err != nil {
		return nil, fmt.Errorf("resolve exact rebase conflict file %s: %w", file.Path, err)
	}
	if pointer.ContentHash != file.ContentHash || pointer.ByteSize != file.ByteSize ||
		rawFileContentHash(value) != file.ContentHash || int64(len(value)) != file.ByteSize {
		return nil, rebaseStoreContract("conflict file bytes differ from immutable tree facts", nil)
	}
	return &CandidateRebaseFileContent{
		ContentHash: file.ContentHash, ByteSize: file.ByteSize,
		Encoding: "base64", Data: base64.StdEncoding.EncodeToString(value),
	}, nil
}

func (service *CandidateRebaseService) loadSuccessor(
	ctx context.Context,
	rebase CandidateRebase,
) (CandidateWorkspace, error) {
	record, err := service.candidates.LoadMutationCandidate(ctx, rebase.ProjectID, rebase.SuccessorCandidateID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CandidateWorkspace{}, ErrCandidateNotFound
	}
	if err != nil {
		return CandidateWorkspace{}, fmt.Errorf("load rebase successor Candidate: %w", err)
	}
	if record.Candidate.ID != rebase.SuccessorCandidateID ||
		record.Candidate.BuildManifest.ID != rebase.TargetBuildManifestID ||
		record.Candidate.BaseTreeHash != rebase.TargetTreeHash {
		return CandidateWorkspace{}, rebaseStoreContract("successor Candidate exact lineage drifted", nil)
	}
	return record.Candidate, nil
}

func normalizeStartCandidateRebaseInput(input StartCandidateRebaseInput) (StartCandidateRebaseInput, error) {
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.PredecessorCandidateID = strings.TrimSpace(input.PredecessorCandidateID)
	input.TargetBuildManifestID = strings.TrimSpace(input.TargetBuildManifestID)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if !validUUID(input.ProjectID) || !validUUID(input.PredecessorCandidateID) ||
		!validUUID(input.TargetBuildManifestID) || !validUUID(input.ActorID) ||
		!bootstrapOperationPattern.MatchString(input.OperationID) || input.ExpectedCandidateVersion == 0 ||
		input.ExpectedSessionEpoch == 0 || !postgresBigint(input.ExpectedCandidateVersion) ||
		!postgresBigint(input.ExpectedSessionEpoch) || !postgresBigint(input.ExpectedWriterLeaseEpoch) {
		return StartCandidateRebaseInput{}, ErrInvalidRebase
	}
	return input, nil
}

func normalizeResolveCandidateRebaseConflictInput(
	input ResolveCandidateRebaseConflictInput,
) (ResolveCandidateRebaseConflictInput, error) {
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RebaseID = strings.TrimSpace(input.RebaseID)
	input.ConflictID = strings.TrimSpace(input.ConflictID)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.Mode = strings.TrimSpace(input.Mode)
	if !validUUID(input.ProjectID) || !validUUID(input.RebaseID) || !validUUID(input.ConflictID) ||
		!validUUID(input.ActorID) || input.ExpectedConflictVersion != 1 ||
		(input.Strategy != CandidateRebaseUsePredecessor && input.Strategy != CandidateRebaseUseTarget &&
			input.Strategy != CandidateRebaseUseCurrent) ||
		(input.Mode != "" && input.Mode != "100644" && input.Mode != "100755") {
		return ResolveCandidateRebaseConflictInput{}, ErrInvalidRebase
	}
	return input, nil
}

func validateRebasePredecessor(candidate CandidateWorkspace, input StartCandidateRebaseInput) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.ProjectID != input.ProjectID || candidate.ID != input.PredecessorCandidateID ||
		candidate.BuildManifest.ID == input.TargetBuildManifestID || candidate.Status != CandidateActive ||
		candidate.Conflicted || candidate.Stale || candidate.RebaseRequired || candidate.Version != input.ExpectedCandidateVersion ||
		candidate.SessionEpoch != input.ExpectedSessionEpoch ||
		candidate.WriterLeaseEpoch != input.ExpectedWriterLeaseEpoch {
		return ErrCandidateRebaseState
	}
	return nil
}

func existingCandidateRebaseMatches(
	rebase CandidateRebase,
	rebaseID string,
	input StartCandidateRebaseInput,
) error {
	if rebase.ID != rebaseID || rebase.ProjectID != input.ProjectID || rebase.OperationID != input.OperationID ||
		rebase.PredecessorCandidateID != input.PredecessorCandidateID ||
		rebase.TargetBuildManifestID != input.TargetBuildManifestID || rebase.CreatedBy != input.ActorID {
		return ErrCandidateRebaseReplay
	}
	return nil
}

func candidateRebaseID(input StartCandidateRebaseInput) string {
	value := strings.Join([]string{
		"candidate-rebase/v1", input.ProjectID, input.PredecessorCandidateID,
		input.TargetBuildManifestID, input.ActorID, input.OperationID,
	}, "\x00")
	digest := sha256.Sum256([]byte(value))
	identifier, _ := uuid.FromBytes(digest[:16])
	identifier[6] = (identifier[6] & 0x0f) | 0x50
	identifier[8] = (identifier[8] & 0x3f) | 0x80
	return identifier.String()
}

func findCandidateRebaseConflict(
	rebase CandidateRebase,
	conflictID string,
) (CandidateRebaseConflictRecord, bool) {
	for _, conflict := range rebase.Conflicts {
		if conflict.ID == conflictID {
			return conflict, true
		}
	}
	return CandidateRebaseConflictRecord{}, false
}

func conflictResolutionMatches(
	conflict CandidateRebaseConflictRecord,
	strategy CandidateRebaseResolutionStrategy,
	selected *TreeFile,
) bool {
	return conflict.State == CandidateRebaseConflictResolved && conflict.ResolutionStrategy != nil &&
		*conflict.ResolutionStrategy == strategy && equalTreeFile(conflict.ResolutionFile, selected) &&
		conflict.ResolutionDeleted != nil && *conflict.ResolutionDeleted == (selected == nil)
}
