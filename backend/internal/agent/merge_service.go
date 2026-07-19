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

var patchMergeNamespace = uuid.MustParse("bf2422d2-2af4-5b3c-9f0c-fb17286fba4e")

type PatchMergeSource interface {
	ResolveAttemptProject(context.Context, string) (string, error)
	GetAttempt(context.Context, string, string) (AgentAttempt, error)
	GetTaskCapsule(context.Context, string, string) (TaskCapsule, error)
}

type PatchMergeReview interface {
	ReadEvidence(context.Context, string, string, EvidenceKind) (EvidenceReadResult, error)
}

type PatchMergeTreeResolver interface {
	ResolveExactTree(context.Context, TaskCapsule) (repository.TreeManifest, error)
}

type PatchMergeWorkspace interface {
	Tree(context.Context, string, string, string) (sandbox.RepositoryView, error)
	MutateAgentFiles(context.Context, sandbox.FileBatchMutationInput) (sandbox.FileBatchMutationResult, error)
}

type PatchMergePersistence interface {
	SavePatchMergePlan(context.Context, PatchMergePlanRecord) (PatchMergePlanRecord, bool, error)
	FindPatchMergePlanByOperation(context.Context, string, string, string) (PatchMergePlanRecord, bool, error)
	GetPatchMergePlan(context.Context, string, string) (PatchMergePlanRecord, error)
	SavePatchMergeApplication(context.Context, PatchMergeApplication) (PatchMergeApplication, bool, error)
	GetPatchMergeApplication(context.Context, string, string) (PatchMergeApplication, bool, error)
}

type MergePatchInput struct {
	AttemptID                string
	ActorID                  string
	OperationID              string
	ExpectedAttemptVersion   uint64
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
}

type MergePatchResult struct {
	Plan        PatchMergePlanRecord   `json:"plan"`
	Application *PatchMergeApplication `json:"application,omitempty"`
	Session     *sandbox.SessionView   `json:"session,omitempty"`
	Replayed    bool                   `json:"replayed"`
}

type PatchMergeService struct {
	source    PatchMergeSource
	review    PatchMergeReview
	trees     PatchMergeTreeResolver
	workspace PatchMergeWorkspace
	store     PatchMergePersistence
	access    ProjectAuthorizer
	now       func() time.Time
}

func NewPatchMergeService(
	source PatchMergeSource,
	review PatchMergeReview,
	trees PatchMergeTreeResolver,
	workspace PatchMergeWorkspace,
	store PatchMergePersistence,
	access ProjectAuthorizer,
	now func() time.Time,
) (*PatchMergeService, error) {
	if source == nil || review == nil || trees == nil || workspace == nil || store == nil || access == nil || now == nil {
		return nil, errors.New("Agent patch merge source, review, tree, workspace, persistence, access, and clock are required")
	}
	return &PatchMergeService{
		source: source, review: review, trees: trees, workspace: workspace,
		store: store, access: access, now: now,
	}, nil
}

func (service *PatchMergeService) MergePatch(
	ctx context.Context,
	input MergePatchInput,
) (MergePatchResult, error) {
	input, err := normalizeMergePatchInput(input)
	if err != nil {
		return MergePatchResult{}, err
	}
	projectID, err := service.source.ResolveAttemptProject(ctx, input.AttemptID)
	if err != nil {
		return MergePatchResult{}, err
	}
	// Edit authorization precedes evidence, plan, Candidate, and application
	// reads. ResolveAttemptProject reveals no cross-project payload.
	if err := service.access.RequireProjectEdit(ctx, projectID, input.ActorID); err != nil {
		return MergePatchResult{}, fmt.Errorf("authorize Agent patch merge: %w", err)
	}
	existing, found, err := service.store.FindPatchMergePlanByOperation(
		ctx, projectID, input.ActorID, input.OperationID,
	)
	if err != nil {
		return MergePatchResult{}, err
	}
	if found {
		if !patchMergeRequestMatches(existing, input) {
			return MergePatchResult{}, ErrPatchMergeReplay
		}
		return service.resume(ctx, existing, true)
	}

	attempt, err := service.source.GetAttempt(ctx, projectID, input.AttemptID)
	if err != nil {
		return MergePatchResult{}, err
	}
	if attempt.State != AttemptReviewReady || attempt.Version != input.ExpectedAttemptVersion ||
		attempt.Evidence.Patch == nil || attempt.Evidence.Validation == nil {
		return MergePatchResult{}, ErrPatchMergeFenced
	}
	evidence, err := service.review.ReadEvidence(ctx, attempt.ID, input.ActorID, EvidencePatch)
	if err != nil {
		return MergePatchResult{}, err
	}
	if !equalJSON(evidence.Attempt, attempt) || evidence.Reference != *attempt.Evidence.Patch {
		return MergePatchResult{}, fmt.Errorf("%w: review evidence Attempt changed", ErrPatchMergeFenced)
	}
	var patch PlatformPatch
	if err := decodeStrictJSON(evidence.Value, &patch); err != nil {
		return MergePatchResult{}, err
	}
	patch, err = ParsePlatformPatch(patch)
	if err != nil || patch.AttemptID != attempt.ID || patch.ProjectID != projectID ||
		patch.CandidateID != attempt.CandidateID || patch.TaskCapsule != attempt.TaskCapsule ||
		patch.ConfigurationHash != attempt.ConfigurationHash || patch.BaseTreeHash != attempt.BaseCandidateTreeHash {
		return MergePatchResult{}, fmt.Errorf("%w: exact patch binding", ErrPatchMergeInvalid)
	}
	capsule, err := service.source.GetTaskCapsule(ctx, projectID, attempt.TaskCapsule.ID)
	if err != nil {
		return MergePatchResult{}, err
	}
	if capsule.ExactReference() != attempt.TaskCapsule || capsule.CandidateID != attempt.CandidateID ||
		capsule.SandboxSessionID != attempt.SandboxSessionID || capsule.BaseCandidateTreeHash != patch.BaseTreeHash {
		return MergePatchResult{}, fmt.Errorf("%w: TaskCapsule binding", ErrPatchMergeFenced)
	}
	base, err := service.trees.ResolveExactTree(ctx, capsule)
	if err != nil {
		return MergePatchResult{}, err
	}
	view, err := service.workspace.Tree(ctx, projectID, attempt.SandboxSessionID, input.ActorID)
	if err != nil {
		return MergePatchResult{}, err
	}
	if err := validatePatchMergeWorkspace(view, attempt, input, service.now().UTC()); err != nil {
		return MergePatchResult{}, err
	}
	mergeID := deterministicPatchMergeID(projectID, input.ActorID, input.OperationID)
	mergePlan, err := PlanPlatformPatchMerge(base, view.Candidate.CurrentTree, patch, mergeID)
	if err != nil {
		return MergePatchResult{}, err
	}
	record, err := NewPatchMergePlanRecord(NewPatchMergePlanInput{
		ID: mergeID, OperationID: input.OperationID, ProjectID: projectID,
		SandboxSessionID: attempt.SandboxSessionID, CandidateID: attempt.CandidateID,
		AttemptID: attempt.ID, AttemptVersion: attempt.Version,
		PatchReference: evidence.Reference, PatchRawHash: evidence.RawHash,
		PatchContentHash:                 patch.ContentHash,
		ExpectedSessionVersion:           view.Session.Version,
		ExpectedSessionEpoch:             view.Session.SessionEpoch,
		ExpectedCandidateVersion:         view.Candidate.Version,
		ExpectedCandidateJournalSequence: view.Candidate.JournalSequence,
		ExpectedWriterLeaseEpoch:         view.Candidate.WriterLeaseEpoch,
		CreatedBy:                        input.ActorID,
	}, mergePlan, service.now().UTC())
	if err != nil {
		return MergePatchResult{}, err
	}
	persisted, replayed, err := service.store.SavePatchMergePlan(ctx, record)
	if err != nil {
		return MergePatchResult{}, err
	}
	return service.resume(ctx, persisted, replayed)
}

func (service *PatchMergeService) resume(
	ctx context.Context,
	plan PatchMergePlanRecord,
	replayed bool,
) (MergePatchResult, error) {
	plan, err := ParsePatchMergePlanRecord(plan)
	if err != nil {
		return MergePatchResult{}, err
	}
	result := MergePatchResult{Plan: plan, Replayed: replayed}
	application, found, err := service.store.GetPatchMergeApplication(ctx, plan.ProjectID, plan.ID)
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

	mutation, err := service.workspace.MutateAgentFiles(ctx, sandbox.FileBatchMutationInput{
		ProjectID: plan.ProjectID, SessionID: plan.SandboxSessionID, CandidateID: plan.CandidateID,
		ActorID: plan.CreatedBy, ExpectedSessionVersion: plan.ExpectedSessionVersion,
		ExpectedSessionEpoch:     plan.ExpectedSessionEpoch,
		ExpectedCandidateVersion: plan.ExpectedCandidateVersion,
		ExpectedWriterLeaseEpoch: plan.ExpectedWriterLeaseEpoch,
		Operations:               cloneMergeOperations(plan.Operations),
	})
	if err != nil {
		if len(mutation.Mutation.Entries) != 0 {
			return result, errors.Join(ErrPatchMergeReconciliation, err)
		}
		return result, err
	}
	created, err := NewPatchMergeApplication(plan, mutation.Mutation, plan.CreatedBy, service.now().UTC())
	if err != nil {
		return result, errors.Join(ErrPatchMergeReconciliation, err)
	}
	persisted, applicationReplayed, err := service.store.SavePatchMergeApplication(ctx, created)
	if err != nil {
		return result, errors.Join(ErrPatchMergeReconciliation, err)
	}
	result.Application = &persisted
	session := mutation.Session
	result.Session = &session
	result.Replayed = result.Replayed || mutation.Mutation.Recovered || applicationReplayed
	return result, nil
}

func normalizeMergePatchInput(input MergePatchInput) (MergePatchInput, error) {
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if !validUUIDs(input.AttemptID, input.ActorID) ||
		!agentOperationPattern.MatchString(input.OperationID) ||
		input.ExpectedAttemptVersion == 0 || input.ExpectedSessionVersion == 0 ||
		input.ExpectedSessionEpoch == 0 || input.ExpectedCandidateVersion == 0 ||
		input.ExpectedWriterLeaseEpoch == 0 || input.ExpectedAttemptVersion > maxPatchMergeBigint ||
		input.ExpectedSessionVersion > maxPatchMergeBigint || input.ExpectedSessionEpoch > maxPatchMergeBigint ||
		input.ExpectedCandidateVersion > maxPatchMergeBigint || input.ExpectedWriterLeaseEpoch > maxPatchMergeBigint {
		return MergePatchInput{}, ErrPatchMergeInvalid
	}
	return input, nil
}

func patchMergeRequestMatches(plan PatchMergePlanRecord, input MergePatchInput) bool {
	return plan.AttemptID == input.AttemptID && plan.CreatedBy == input.ActorID &&
		plan.OperationID == input.OperationID && plan.AttemptVersion == input.ExpectedAttemptVersion &&
		plan.ExpectedSessionVersion == input.ExpectedSessionVersion &&
		plan.ExpectedSessionEpoch == input.ExpectedSessionEpoch &&
		plan.ExpectedCandidateVersion == input.ExpectedCandidateVersion &&
		plan.ExpectedWriterLeaseEpoch == input.ExpectedWriterLeaseEpoch
}

func validatePatchMergeWorkspace(
	view sandbox.RepositoryView,
	attempt AgentAttempt,
	input MergePatchInput,
	now time.Time,
) error {
	candidate := view.Candidate
	session := view.Session
	if session.ProjectID != attempt.ProjectID || session.ID != attempt.SandboxSessionID ||
		session.Candidate.ID != attempt.CandidateID || candidate.ID != attempt.CandidateID ||
		candidate.ProjectID != attempt.ProjectID || session.Version != input.ExpectedSessionVersion ||
		session.SessionEpoch != input.ExpectedSessionEpoch || candidate.SessionEpoch != input.ExpectedSessionEpoch ||
		candidate.Version != input.ExpectedCandidateVersion ||
		candidate.WriterLeaseEpoch != input.ExpectedWriterLeaseEpoch ||
		session.Candidate.Version != candidate.Version ||
		session.Candidate.JournalSequence != candidate.JournalSequence ||
		session.Candidate.TreeHash != candidate.CurrentTree.TreeHash ||
		candidate.Lease == nil || candidate.Lease.OwnerID != input.ActorID ||
		candidate.Lease.Epoch != input.ExpectedWriterLeaseEpoch || !now.Before(candidate.Lease.ExpiresAt) {
		return ErrPatchMergeFenced
	}
	return nil
}

func deterministicPatchMergeID(projectID, actorID, operationID string) string {
	return uuid.NewSHA1(
		patchMergeNamespace,
		[]byte(projectID+"\x00"+actorID+"\x00"+operationID),
	).String()
}

var _ PatchMergeSource = (*PostgresStore)(nil)
var _ PatchMergeReview = (*ReviewService)(nil)
var _ PatchMergeTreeResolver = (*PostgresCandidateTreeResolver)(nil)
var _ PatchMergeWorkspace = (*sandbox.Facade)(nil)
var _ PatchMergePersistence = (*PostgresStore)(nil)
