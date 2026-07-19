package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

var patchUndoNamespace = uuid.MustParse("e0c134b1-8ef8-5436-b54c-0e4e12595881")

type PatchUndoSource interface {
	ResolvePatchMergeProject(context.Context, string) (string, error)
	GetPatchMergePlan(context.Context, string, string) (PatchMergePlanRecord, error)
	GetPatchMergeApplication(context.Context, string, string) (PatchMergeApplication, bool, error)
}

type PatchUndoTreeReader interface {
	Get(context.Context, string, string, repository.TreeBlobPointer) (repository.TreeManifest, error)
}

type PatchUndoWorkspace interface {
	Tree(context.Context, string, string, string) (sandbox.RepositoryView, error)
	MutateRestoreFiles(context.Context, sandbox.FileBatchMutationInput) (sandbox.FileBatchMutationResult, error)
}

type PatchUndoPersistence interface {
	SavePatchUndoPlan(context.Context, PatchUndoPlanRecord) (PatchUndoPlanRecord, bool, error)
	FindPatchUndoPlanByOperation(context.Context, string, string, string) (PatchUndoPlanRecord, bool, error)
	GetPatchUndoPlan(context.Context, string, string) (PatchUndoPlanRecord, error)
	SavePatchUndoApplication(context.Context, PatchUndoApplication) (PatchUndoApplication, bool, error)
	GetPatchUndoApplication(context.Context, string, string) (PatchUndoApplication, bool, error)
}

type UndoPatchInput struct {
	MergeID                  string
	ExpectedMergeContentHash string
	ActorID                  string
	OperationID              string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
}

type UndoPatchResult struct {
	Plan        PatchUndoPlanRecord   `json:"plan"`
	Application *PatchUndoApplication `json:"application,omitempty"`
	Session     *sandbox.SessionView  `json:"session,omitempty"`
	Replayed    bool                  `json:"replayed"`
}

type PatchUndoService struct {
	source    PatchUndoSource
	trees     PatchUndoTreeReader
	workspace PatchUndoWorkspace
	store     PatchUndoPersistence
	access    ProjectAuthorizer
	now       func() time.Time
}

func NewPatchUndoService(
	source PatchUndoSource,
	trees PatchUndoTreeReader,
	workspace PatchUndoWorkspace,
	store PatchUndoPersistence,
	access ProjectAuthorizer,
	now func() time.Time,
) (*PatchUndoService, error) {
	if source == nil || trees == nil || workspace == nil || store == nil || access == nil || now == nil {
		return nil, errors.New("Agent patch undo source, tree, workspace, persistence, access, and clock are required")
	}
	return &PatchUndoService{
		source: source, trees: trees, workspace: workspace,
		store: store, access: access, now: now,
	}, nil
}

func (service *PatchUndoService) UndoPatch(
	ctx context.Context,
	input UndoPatchInput,
) (UndoPatchResult, error) {
	input, err := normalizeUndoPatchInput(input)
	if err != nil {
		return UndoPatchResult{}, err
	}
	projectID, err := service.source.ResolvePatchMergeProject(ctx, input.MergeID)
	if err != nil {
		return UndoPatchResult{}, err
	}
	// Project authorization precedes all merge, application, tree, Candidate,
	// and undo-plan payload reads.
	if err := service.access.RequireProjectEdit(ctx, projectID, input.ActorID); err != nil {
		return UndoPatchResult{}, fmt.Errorf("authorize Agent patch undo: %w", err)
	}
	existing, found, err := service.store.FindPatchUndoPlanByOperation(
		ctx, projectID, input.ActorID, input.OperationID,
	)
	if err != nil {
		return UndoPatchResult{}, err
	}
	if found {
		if !patchUndoRequestMatches(existing, input) {
			return UndoPatchResult{}, ErrPatchUndoReplay
		}
		return service.resume(ctx, existing, true)
	}

	merge, err := service.source.GetPatchMergePlan(ctx, projectID, input.MergeID)
	if err != nil {
		return UndoPatchResult{}, err
	}
	if merge.ContentHash != input.ExpectedMergeContentHash ||
		merge.Disposition != PatchMergePlanned || merge.CreatedBy != input.ActorID {
		return UndoPatchResult{}, ErrPatchUndoFenced
	}
	application, applied, err := service.source.GetPatchMergeApplication(ctx, projectID, input.MergeID)
	if err != nil {
		return UndoPatchResult{}, err
	}
	if !applied || application.PlanContentHash != merge.ContentHash ||
		application.ProjectID != merge.ProjectID || application.CandidateID != merge.CandidateID ||
		application.AppliedBy != input.ActorID || application.BeforeTree.TreeHash != merge.CurrentTreeHash ||
		application.AfterTree.TreeHash != merge.PlannedTreeHash {
		return UndoPatchResult{}, ErrPatchUndoFenced
	}
	mergeBefore, err := service.trees.Get(
		ctx, projectID, application.BeforeTree.OwnerID, application.BeforeTree,
	)
	if err != nil {
		return UndoPatchResult{}, fmt.Errorf("read Agent merge before tree: %w", err)
	}
	view, err := service.workspace.Tree(ctx, projectID, merge.SandboxSessionID, input.ActorID)
	if err != nil {
		return UndoPatchResult{}, err
	}
	if err := validatePatchUndoWorkspace(view, merge, input, service.now().UTC()); err != nil {
		return UndoPatchResult{}, err
	}
	undoID := deterministicPatchUndoID(projectID, input.ActorID, input.OperationID)
	undoPlan, err := PlanPlatformPatchMergeUndo(mergeBefore, view.Candidate.CurrentTree, merge, undoID)
	if err != nil {
		return UndoPatchResult{}, err
	}
	record, err := NewPatchUndoPlanRecord(NewPatchUndoPlanInput{
		ID: undoID, OperationID: input.OperationID, ProjectID: projectID,
		SandboxSessionID: merge.SandboxSessionID, CandidateID: merge.CandidateID,
		MergeID: merge.ID, MergePlanContentHash: merge.ContentHash,
		MergeApplicationContentHash:      application.ContentHash,
		ExpectedSessionVersion:           view.Session.Version,
		ExpectedSessionEpoch:             view.Session.SessionEpoch,
		ExpectedCandidateVersion:         view.Candidate.Version,
		ExpectedCandidateJournalSequence: view.Candidate.JournalSequence,
		ExpectedWriterLeaseEpoch:         view.Candidate.WriterLeaseEpoch,
		CreatedBy:                        input.ActorID,
	}, undoPlan, service.now().UTC())
	if err != nil {
		return UndoPatchResult{}, err
	}
	persisted, replayed, err := service.store.SavePatchUndoPlan(ctx, record)
	if err != nil {
		return UndoPatchResult{}, err
	}
	return service.resume(ctx, persisted, replayed)
}

func (service *PatchUndoService) resume(
	ctx context.Context,
	plan PatchUndoPlanRecord,
	replayed bool,
) (UndoPatchResult, error) {
	plan, err := ParsePatchUndoPlanRecord(plan)
	if err != nil {
		return UndoPatchResult{}, err
	}
	result := UndoPatchResult{Plan: plan, Replayed: replayed}
	application, found, err := service.store.GetPatchUndoApplication(ctx, plan.ProjectID, plan.ID)
	if err != nil {
		return result, err
	}
	if found {
		result.Application = &application
		result.Replayed = true
		return result, nil
	}
	if plan.Disposition != PatchMergePlanned {
		return result, nil
	}

	mutation, err := service.workspace.MutateRestoreFiles(ctx, sandbox.FileBatchMutationInput{
		ProjectID: plan.ProjectID, SessionID: plan.SandboxSessionID, CandidateID: plan.CandidateID,
		ActorID: plan.CreatedBy, ExpectedSessionVersion: plan.ExpectedSessionVersion,
		ExpectedSessionEpoch:     plan.ExpectedSessionEpoch,
		ExpectedCandidateVersion: plan.ExpectedCandidateVersion,
		ExpectedWriterLeaseEpoch: plan.ExpectedWriterLeaseEpoch,
		Operations:               cloneMergeOperations(plan.Operations),
	})
	if err != nil {
		if len(mutation.Mutation.Entries) != 0 {
			return result, errors.Join(ErrPatchUndoReconciliation, err)
		}
		return result, err
	}
	created, err := NewPatchUndoApplication(plan, mutation.Mutation, plan.CreatedBy, service.now().UTC())
	if err != nil {
		return result, errors.Join(ErrPatchUndoReconciliation, err)
	}
	persisted, applicationReplayed, err := service.store.SavePatchUndoApplication(ctx, created)
	if err != nil {
		return result, errors.Join(ErrPatchUndoReconciliation, err)
	}
	result.Application = &persisted
	session := mutation.Session
	result.Session = &session
	result.Replayed = result.Replayed || mutation.Mutation.Recovered || applicationReplayed
	return result, nil
}

func normalizeUndoPatchInput(input UndoPatchInput) (UndoPatchInput, error) {
	input.MergeID = strings.TrimSpace(input.MergeID)
	input.ExpectedMergeContentHash = strings.TrimSpace(input.ExpectedMergeContentHash)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if !validUUIDs(input.MergeID, input.ActorID) ||
		!sha256Pattern.MatchString(input.ExpectedMergeContentHash) ||
		!agentOperationPattern.MatchString(input.OperationID) ||
		input.ExpectedSessionVersion == 0 || input.ExpectedSessionEpoch == 0 ||
		input.ExpectedCandidateVersion == 0 || input.ExpectedWriterLeaseEpoch == 0 ||
		input.ExpectedSessionVersion > maxPatchMergeBigint ||
		input.ExpectedSessionEpoch > maxPatchMergeBigint ||
		input.ExpectedCandidateVersion > maxPatchMergeBigint ||
		input.ExpectedWriterLeaseEpoch > maxPatchMergeBigint {
		return UndoPatchInput{}, ErrPatchUndoInvalid
	}
	return input, nil
}

func patchUndoRequestMatches(plan PatchUndoPlanRecord, input UndoPatchInput) bool {
	return plan.MergeID == input.MergeID && plan.MergePlanContentHash == input.ExpectedMergeContentHash &&
		plan.CreatedBy == input.ActorID && plan.OperationID == input.OperationID &&
		plan.ExpectedSessionVersion == input.ExpectedSessionVersion &&
		plan.ExpectedSessionEpoch == input.ExpectedSessionEpoch &&
		plan.ExpectedCandidateVersion == input.ExpectedCandidateVersion &&
		plan.ExpectedWriterLeaseEpoch == input.ExpectedWriterLeaseEpoch
}

func validatePatchUndoWorkspace(
	view sandbox.RepositoryView,
	merge PatchMergePlanRecord,
	input UndoPatchInput,
	now time.Time,
) error {
	candidate := view.Candidate
	session := view.Session
	if session.ProjectID != merge.ProjectID || session.ID != merge.SandboxSessionID ||
		session.State != sandbox.StateReady || session.Candidate.ID != merge.CandidateID ||
		candidate.ID != merge.CandidateID || candidate.ProjectID != merge.ProjectID ||
		candidate.Status != repository.CandidateActive || session.Version != input.ExpectedSessionVersion ||
		session.SessionEpoch != input.ExpectedSessionEpoch || candidate.SessionEpoch != input.ExpectedSessionEpoch ||
		candidate.Version != input.ExpectedCandidateVersion ||
		candidate.WriterLeaseEpoch != input.ExpectedWriterLeaseEpoch ||
		session.Candidate.Version != candidate.Version ||
		session.Candidate.JournalSequence != candidate.JournalSequence ||
		session.Candidate.WriterLeaseEpoch != candidate.WriterLeaseEpoch ||
		session.Candidate.TreeHash != candidate.CurrentTree.TreeHash ||
		view.Tree.TreeHash != candidate.CurrentTree.TreeHash ||
		candidate.Lease == nil || candidate.Lease.OwnerID != input.ActorID ||
		candidate.Lease.Epoch != input.ExpectedWriterLeaseEpoch || !now.Before(candidate.Lease.ExpiresAt) {
		return ErrPatchUndoFenced
	}
	return nil
}

func deterministicPatchUndoID(projectID, actorID, operationID string) string {
	return uuid.NewSHA1(
		patchUndoNamespace,
		[]byte(projectID+"\x00"+actorID+"\x00"+operationID),
	).String()
}

var _ PatchUndoSource = (*PostgresStore)(nil)
var _ PatchUndoTreeReader = (*repository.TreeStore)(nil)
var _ PatchUndoWorkspace = (*sandbox.Facade)(nil)
var _ PatchUndoPersistence = (*PostgresStore)(nil)
