package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

type patchMergeSourceFake struct {
	attempt AgentAttempt
	capsule TaskCapsule
}

func (source *patchMergeSourceFake) ResolveAttemptProject(context.Context, string) (string, error) {
	return source.attempt.ProjectID, nil
}

func (source *patchMergeSourceFake) GetAttempt(context.Context, string, string) (AgentAttempt, error) {
	return source.attempt, nil
}

func (source *patchMergeSourceFake) GetTaskCapsule(context.Context, string, string) (TaskCapsule, error) {
	return source.capsule, nil
}

type patchMergeReviewFake struct{ evidence EvidenceReadResult }

func (review *patchMergeReviewFake) ReadEvidence(
	context.Context, string, string, EvidenceKind,
) (EvidenceReadResult, error) {
	return review.evidence, nil
}

type patchMergeTreeFake struct{ tree repository.TreeManifest }

func (trees *patchMergeTreeFake) ResolveExactTree(context.Context, TaskCapsule) (repository.TreeManifest, error) {
	return trees.tree, nil
}

type patchMergeStoreFake struct {
	plan        *PatchMergePlanRecord
	application *PatchMergeApplication
}

func (store *patchMergeStoreFake) SavePatchMergePlan(
	_ context.Context, plan PatchMergePlanRecord,
) (PatchMergePlanRecord, bool, error) {
	parsed, err := ParsePatchMergePlanRecord(plan)
	if err != nil {
		return PatchMergePlanRecord{}, false, err
	}
	if store.plan != nil {
		if !equalJSON(*store.plan, parsed) {
			return PatchMergePlanRecord{}, false, ErrPatchMergeReplay
		}
		return *store.plan, true, nil
	}
	store.plan = &parsed
	return parsed, false, nil
}

func (store *patchMergeStoreFake) FindPatchMergePlanByOperation(
	_ context.Context, projectID, actorID, operationID string,
) (PatchMergePlanRecord, bool, error) {
	if store.plan == nil || store.plan.ProjectID != projectID || store.plan.CreatedBy != actorID ||
		store.plan.OperationID != operationID {
		return PatchMergePlanRecord{}, false, nil
	}
	return *store.plan, true, nil
}

func (store *patchMergeStoreFake) GetPatchMergePlan(
	context.Context, string, string,
) (PatchMergePlanRecord, error) {
	if store.plan == nil {
		return PatchMergePlanRecord{}, ErrPatchMergeNotFound
	}
	return *store.plan, nil
}

func (store *patchMergeStoreFake) SavePatchMergeApplication(
	_ context.Context, application PatchMergeApplication,
) (PatchMergeApplication, bool, error) {
	parsed, err := ParsePatchMergeApplication(application)
	if err != nil {
		return PatchMergeApplication{}, false, err
	}
	if store.application != nil {
		if !equalJSON(*store.application, parsed) {
			return PatchMergeApplication{}, false, ErrPatchMergeReplay
		}
		return *store.application, true, nil
	}
	store.application = &parsed
	return parsed, false, nil
}

func (store *patchMergeStoreFake) GetPatchMergeApplication(
	context.Context, string, string,
) (PatchMergeApplication, bool, error) {
	if store.application == nil {
		return PatchMergeApplication{}, false, nil
	}
	return *store.application, true, nil
}

type patchMergeWorkspaceFake struct {
	view        sandbox.RepositoryView
	now         time.Time
	mutateCalls int
}

func (workspace *patchMergeWorkspaceFake) Tree(
	context.Context, string, string, string,
) (sandbox.RepositoryView, error) {
	return workspace.view, nil
}

func (workspace *patchMergeWorkspaceFake) MutateAgentFiles(
	_ context.Context,
	input sandbox.FileBatchMutationInput,
) (sandbox.FileBatchMutationResult, error) {
	workspace.mutateCalls++
	candidate := workspace.view.Candidate
	before := mergeTreePointer(candidate.ID, candidate.CurrentTree, "before")
	entries := make([]repository.JournalEntry, len(input.Operations))
	for index, operation := range input.Operations {
		var err error
		candidate, entries[index], err = candidate.Apply(
			candidate.Version, input.ExpectedSessionEpoch, input.ExpectedWriterLeaseEpoch,
			input.ActorID, "agent", operation, workspace.now,
		)
		if err != nil {
			return sandbox.FileBatchMutationResult{}, err
		}
	}
	after := mergeTreePointer(candidate.ID, candidate.CurrentTree, "after")
	mutation := repository.BatchMutationResult{
		Entries: entries, BeforeTree: before, AfterTree: after,
		FinalCandidateVersion: candidate.Version,
	}
	workspace.view.Candidate = candidate
	workspace.view.Session.Candidate.Version = candidate.Version
	workspace.view.Session.Candidate.JournalSequence = candidate.JournalSequence
	workspace.view.Session.Candidate.TreeHash = candidate.CurrentTree.TreeHash
	return sandbox.FileBatchMutationResult{Session: workspace.view.Session, Mutation: mutation}, nil
}

func TestPatchMergeServiceAppliesExactPlanAndReplaysApplication(t *testing.T) {
	fixture := newPatchMergeServiceFixture(t, false)
	fixture.service.now = func() time.Time { return fixture.workspace.now.Add(321 * time.Nanosecond) }
	result, err := fixture.service.MergePatch(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Disposition != PatchMergePlanned || result.Application == nil ||
		len(result.Plan.Operations) != 1 || fixture.workspace.mutateCalls != 1 || result.Replayed {
		t.Fatalf("first merge result=%#v calls=%d", result, fixture.workspace.mutateCalls)
	}
	if result.Application.BeforeTree.TreeHash != result.Plan.CurrentTreeHash ||
		result.Application.AfterTree.TreeHash != result.Plan.PlannedTreeHash ||
		result.Application.CandidateVersionTo != fixture.input.ExpectedCandidateVersion+1 {
		t.Fatalf("application did not bind plan: %#v", result.Application)
	}
	if result.Plan.CreatedAt != canonicalDatabaseTime(result.Plan.CreatedAt) ||
		result.Application.AppliedAt != canonicalDatabaseTime(result.Application.AppliedAt) {
		t.Fatalf("merge timestamps are not PostgreSQL-canonical: plan=%s application=%s",
			result.Plan.CreatedAt, result.Application.AppliedAt)
	}

	replayed, err := fixture.service.MergePatch(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Application == nil || fixture.workspace.mutateCalls != 1 ||
		replayed.Plan.ContentHash != result.Plan.ContentHash ||
		replayed.Application.ContentHash != result.Application.ContentHash {
		t.Fatalf("replayed merge=%#v calls=%d", replayed, fixture.workspace.mutateCalls)
	}

	changed := fixture.input
	changed.ExpectedSessionVersion++
	if _, err := fixture.service.MergePatch(context.Background(), changed); !errors.Is(err, ErrPatchMergeReplay) {
		t.Fatalf("changed idempotency replay error=%v", err)
	}
}

func TestPatchMergeServicePersistsConflictWithoutPartialApplication(t *testing.T) {
	fixture := newPatchMergeServiceFixture(t, true)
	result, err := fixture.service.MergePatch(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Disposition != PatchMergeConflicted || len(result.Plan.Conflicts) != 1 ||
		len(result.Plan.Operations) != 0 || result.Application != nil || fixture.workspace.mutateCalls != 0 {
		t.Fatalf("conflicted merge=%#v calls=%d", result, fixture.workspace.mutateCalls)
	}
	conflict := result.Plan.Conflicts[0]
	if conflict.Current == conflict.Base || conflict.Current == conflict.Proposed {
		t.Fatalf("conflict lost three-way states: %#v", conflict)
	}
}

type patchMergeServiceFixture struct {
	service   *PatchMergeService
	workspace *patchMergeWorkspaceFake
	input     MergePatchInput
}

func newPatchMergeServiceFixture(t *testing.T, conflict bool) patchMergeServiceFixture {
	t.Helper()
	fixture := newAgentFixture(t)
	base := mustMergeTree(t, mergeFile("apps/web/features/conversation/a.ts", "a", 1))
	fixture.contextInput.BaseCandidateTreeHash = base.TreeHash
	fixture.taskInput.BaseCandidateTreeHash = base.TreeHash
	fixture.taskInput.CandidateSessionEpoch = 1
	fixture.taskInput.CandidateWriterLeaseEpoch = 1
	fixture.taskInput.WriteSet = []string{"apps/web/features/conversation"}
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := repository.RepositorySnapshot{
		ID: uuid.NewString(), ProjectID: fixture.taskInput.ProjectID,
		BuildManifest:     repository.ExactReference{ID: uuid.NewString(), ContentHash: testHash("3")},
		BuildContract:     fixture.taskInput.BuildContract,
		FullStackTemplate: repository.ExactReference{ID: uuid.NewString(), ContentHash: testHash("4")},
		Tree:              base, CreatedBy: fixture.actorID, CreatedAt: fixture.now.Add(-time.Minute),
	}
	candidate, err := repository.NewCandidate(
		fixture.taskInput.CandidateID, snapshot, fixture.actorID, fixture.now.Add(-45*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err = candidate.AcquireLease(
		candidate.Version, fixture.actorID, 20*time.Minute, fixture.now.Add(-30*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.taskInput.CandidateVersion = candidate.Version
	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: testExecutor(),
	}, capsule, pack, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	attempt.State = AttemptReviewReady
	attempt.Version = 8

	operation := repository.FileOperation{
		ID: "agent-patch-upsert", Kind: repository.OperationUpsert,
		Path: "apps/web/features/conversation/a.ts", ExpectedHash: testHash("a"),
		ContentHash: testHash("b"), ByteSize: 1, Mode: "100644",
	}
	proposed, err := repository.ApplyOperation(base, operation)
	if err != nil {
		t.Fatal(err)
	}
	patch := PlatformPatch{
		SchemaVersion: PlatformPatchSchemaVersion, AttemptID: attempt.ID,
		ProjectID: attempt.ProjectID, CandidateID: attempt.CandidateID,
		TaskCapsule: attempt.TaskCapsule, ConfigurationHash: attempt.ConfigurationHash,
		BaseTreeHash: base.TreeHash, ProposedTreeHash: proposed.TreeHash,
		Operations: []repository.FileOperation{operation}, ChangedBytes: 1,
	}
	hash, err := domain.CanonicalHash(platformPatchPayload(patch))
	if err != nil {
		t.Fatal(err)
	}
	patch.ContentHash = "sha256:" + hash
	raw, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	patchReference := BlobReference{
		Store: AgentEvidenceStore, OwnerID: attempt.ID, Ref: uuid.NewString(),
		ContentHash: testHash("8"), ByteSize: int64(len(raw)),
	}
	validationReference := BlobReference{
		Store: AgentEvidenceStore, OwnerID: attempt.ID, Ref: uuid.NewString(),
		ContentHash: testHash("9"), ByteSize: 64,
	}
	attempt.Evidence.Patch = &patchReference
	attempt.Evidence.Validation = &validationReference

	if conflict {
		current, currentErr := repository.NewTree([]repository.TreeFile{
			mergeFile("apps/web/features/conversation/a.ts", "c", 1),
		})
		if currentErr != nil {
			t.Fatal(currentErr)
		}
		candidate.CurrentTree = current
	}
	session := sandbox.SessionView{
		ID: attempt.SandboxSessionID, ProjectID: attempt.ProjectID,
		Version: 5, SessionEpoch: candidate.SessionEpoch,
		Candidate: sandbox.CandidateState{
			ID: candidate.ID, Version: candidate.Version, JournalSequence: candidate.JournalSequence,
			SessionEpoch: candidate.SessionEpoch, WriterLeaseEpoch: candidate.WriterLeaseEpoch,
			TreeHash: candidate.CurrentTree.TreeHash,
		},
	}
	workspace := &patchMergeWorkspaceFake{
		view: sandbox.RepositoryView{Session: session, Candidate: candidate, Tree: candidate.CurrentTree},
		now:  fixture.now.Add(time.Second),
	}
	source := &patchMergeSourceFake{attempt: attempt, capsule: capsule}
	review := &patchMergeReviewFake{evidence: EvidenceReadResult{
		Attempt: attempt, Kind: EvidencePatch, Reference: patchReference,
		MediaType: "application/json", RawHash: rawEvidenceHash(raw), Value: raw,
	}}
	store := &patchMergeStoreFake{}
	service, err := NewPatchMergeService(
		source, review, &patchMergeTreeFake{tree: base}, workspace, store,
		&agentAccessFake{}, func() time.Time { return fixture.now.Add(time.Second) },
	)
	if err != nil {
		t.Fatal(err)
	}
	return patchMergeServiceFixture{
		service: service, workspace: workspace,
		input: MergePatchInput{
			AttemptID: attempt.ID, ActorID: fixture.actorID, OperationID: "merge-request-1",
			ExpectedAttemptVersion: attempt.Version, ExpectedSessionVersion: session.Version,
			ExpectedSessionEpoch: session.SessionEpoch, ExpectedCandidateVersion: candidate.Version,
			ExpectedWriterLeaseEpoch: candidate.WriterLeaseEpoch,
		},
	}
}

func mergeTreePointer(ownerID string, tree repository.TreeManifest, suffix string) repository.TreeBlobPointer {
	var size int64
	for _, file := range tree.Files {
		size += file.ByteSize
	}
	return repository.TreeBlobPointer{
		Store: repository.TreeContentStore, OwnerID: ownerID, Ref: "merge-tree-" + suffix,
		ContentObjectHash: testHash("7"), TreeHash: tree.TreeHash,
		FileCount: len(tree.Files), ByteSize: size,
	}
}
