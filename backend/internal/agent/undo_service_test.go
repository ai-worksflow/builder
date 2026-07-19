package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

type patchUndoSourceFake struct {
	merge       PatchMergePlanRecord
	application PatchMergeApplication
}

func (source *patchUndoSourceFake) ResolvePatchMergeProject(context.Context, string) (string, error) {
	return source.merge.ProjectID, nil
}

func (source *patchUndoSourceFake) GetPatchMergePlan(
	context.Context, string, string,
) (PatchMergePlanRecord, error) {
	return source.merge, nil
}

func (source *patchUndoSourceFake) GetPatchMergeApplication(
	context.Context, string, string,
) (PatchMergeApplication, bool, error) {
	return source.application, true, nil
}

type patchUndoTreeFake struct{ tree repository.TreeManifest }

func (trees *patchUndoTreeFake) Get(
	context.Context, string, string, repository.TreeBlobPointer,
) (repository.TreeManifest, error) {
	return trees.tree, nil
}

type patchUndoStoreFake struct {
	plan        *PatchUndoPlanRecord
	application *PatchUndoApplication
}

func (store *patchUndoStoreFake) SavePatchUndoPlan(
	_ context.Context, plan PatchUndoPlanRecord,
) (PatchUndoPlanRecord, bool, error) {
	parsed, err := ParsePatchUndoPlanRecord(plan)
	if err != nil {
		return PatchUndoPlanRecord{}, false, err
	}
	if store.plan != nil {
		if !equalJSON(*store.plan, parsed) {
			return PatchUndoPlanRecord{}, false, ErrPatchUndoReplay
		}
		return *store.plan, true, nil
	}
	store.plan = &parsed
	return parsed, false, nil
}

func (store *patchUndoStoreFake) FindPatchUndoPlanByOperation(
	_ context.Context, projectID, actorID, operationID string,
) (PatchUndoPlanRecord, bool, error) {
	if store.plan == nil || store.plan.ProjectID != projectID || store.plan.CreatedBy != actorID ||
		store.plan.OperationID != operationID {
		return PatchUndoPlanRecord{}, false, nil
	}
	return *store.plan, true, nil
}

func (store *patchUndoStoreFake) GetPatchUndoPlan(
	context.Context, string, string,
) (PatchUndoPlanRecord, error) {
	if store.plan == nil {
		return PatchUndoPlanRecord{}, ErrPatchUndoNotFound
	}
	return *store.plan, nil
}

func (store *patchUndoStoreFake) SavePatchUndoApplication(
	_ context.Context, application PatchUndoApplication,
) (PatchUndoApplication, bool, error) {
	parsed, err := ParsePatchUndoApplication(application)
	if err != nil {
		return PatchUndoApplication{}, false, err
	}
	if store.application != nil {
		if !equalJSON(*store.application, parsed) {
			return PatchUndoApplication{}, false, ErrPatchUndoReplay
		}
		return *store.application, true, nil
	}
	store.application = &parsed
	return parsed, false, nil
}

func (store *patchUndoStoreFake) GetPatchUndoApplication(
	context.Context, string, string,
) (PatchUndoApplication, bool, error) {
	if store.application == nil {
		return PatchUndoApplication{}, false, nil
	}
	return *store.application, true, nil
}

type patchUndoWorkspaceFake struct {
	view        sandbox.RepositoryView
	now         time.Time
	mutateCalls int
}

func (workspace *patchUndoWorkspaceFake) Tree(
	context.Context, string, string, string,
) (sandbox.RepositoryView, error) {
	return workspace.view, nil
}

func (workspace *patchUndoWorkspaceFake) MutateRestoreFiles(
	_ context.Context,
	input sandbox.FileBatchMutationInput,
) (sandbox.FileBatchMutationResult, error) {
	workspace.mutateCalls++
	candidate := workspace.view.Candidate
	before := mergeTreePointer(candidate.ID, candidate.CurrentTree, "undo-before")
	entries := make([]repository.JournalEntry, len(input.Operations))
	for index, operation := range input.Operations {
		var err error
		candidate, entries[index], err = candidate.Apply(
			candidate.Version, input.ExpectedSessionEpoch, input.ExpectedWriterLeaseEpoch,
			input.ActorID, "restore", operation, workspace.now,
		)
		if err != nil {
			return sandbox.FileBatchMutationResult{}, err
		}
	}
	after := mergeTreePointer(candidate.ID, candidate.CurrentTree, "undo-after")
	mutation := repository.BatchMutationResult{
		Entries: entries, BeforeTree: before, AfterTree: after,
		FinalCandidateVersion: candidate.Version,
	}
	workspace.view.Candidate = candidate
	workspace.view.Tree = candidate.CurrentTree
	workspace.view.Session.Version++
	workspace.view.Session.Candidate.Version = candidate.Version
	workspace.view.Session.Candidate.JournalSequence = candidate.JournalSequence
	workspace.view.Session.Candidate.TreeHash = candidate.CurrentTree.TreeHash
	return sandbox.FileBatchMutationResult{Session: workspace.view.Session, Mutation: mutation}, nil
}

func TestPatchUndoServiceRestoresExactMergeAndReplaysApplication(t *testing.T) {
	fixture := newPatchUndoServiceFixture(t, "planned")
	result, err := fixture.service.UndoPatch(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Disposition != PatchMergePlanned || result.Application == nil ||
		len(result.Plan.Operations) != 1 || fixture.workspace.mutateCalls != 1 || result.Replayed {
		t.Fatalf("first undo result=%#v calls=%d", result, fixture.workspace.mutateCalls)
	}
	if result.Application.BeforeTree.TreeHash != result.Plan.CurrentTreeHash ||
		result.Application.AfterTree.TreeHash != result.Plan.PlannedTreeHash ||
		fixture.workspace.view.Candidate.CurrentTree.TreeHash != result.Plan.MergeBeforeTreeHash {
		t.Fatalf("undo application did not restore exact merge: %#v", result.Application)
	}

	replayed, err := fixture.service.UndoPatch(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Application == nil || fixture.workspace.mutateCalls != 1 ||
		replayed.Plan.ContentHash != result.Plan.ContentHash ||
		replayed.Application.ContentHash != result.Application.ContentHash {
		t.Fatalf("replayed undo=%#v calls=%d", replayed, fixture.workspace.mutateCalls)
	}

	changed := fixture.input
	changed.ExpectedSessionVersion++
	if _, err := fixture.service.UndoPatch(context.Background(), changed); !errors.Is(err, ErrPatchUndoReplay) {
		t.Fatalf("changed idempotency replay error=%v", err)
	}
}

func TestPatchUndoServicePersistsConflictWithoutRestoreWrites(t *testing.T) {
	fixture := newPatchUndoServiceFixture(t, "conflicted")
	result, err := fixture.service.UndoPatch(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Disposition != PatchMergeConflicted || len(result.Plan.Conflicts) != 1 ||
		len(result.Plan.Operations) != 0 || result.Application != nil || fixture.workspace.mutateCalls != 0 {
		t.Fatalf("conflicted undo=%#v calls=%d", result, fixture.workspace.mutateCalls)
	}
}

func TestPatchUndoServicePersistsNoopWhenMergeIsAlreadyRestored(t *testing.T) {
	fixture := newPatchUndoServiceFixture(t, "noop")
	result, err := fixture.service.UndoPatch(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Disposition != PatchMergeNoop || len(result.Plan.Operations) != 0 ||
		len(result.Plan.Conflicts) != 0 || result.Application != nil || fixture.workspace.mutateCalls != 0 {
		t.Fatalf("no-op undo=%#v calls=%d", result, fixture.workspace.mutateCalls)
	}
	raw, err := json.Marshal(result.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || jsonContainsNullArray(raw, "operations") || jsonContainsNullArray(raw, "conflicts") {
		t.Fatalf("empty collections must be canonical arrays: %s", raw)
	}
}

type patchUndoServiceFixture struct {
	service   *PatchUndoService
	workspace *patchUndoWorkspaceFake
	input     UndoPatchInput
}

func newPatchUndoServiceFixture(t *testing.T, state string) patchUndoServiceFixture {
	t.Helper()
	mergeFixture := newPatchMergeServiceFixture(t, false)
	merged, err := mergeFixture.service.MergePatch(context.Background(), mergeFixture.input)
	if err != nil || merged.Application == nil {
		t.Fatalf("prepare applied merge: result=%#v err=%v", merged, err)
	}
	mergeBefore := mergeFixture.service.trees.(*patchMergeTreeFake).tree
	view := mergeFixture.workspace.view
	view.Session.State = sandbox.StateReady
	view.Tree = view.Candidate.CurrentTree
	switch state {
	case "conflicted":
		changed, treeErr := repository.NewTree([]repository.TreeFile{
			mergeFile("apps/web/features/conversation/a.ts", "c", 1),
		})
		if treeErr != nil {
			t.Fatal(treeErr)
		}
		view.Candidate.CurrentTree = changed
		view.Tree = changed
		view.Session.Candidate.TreeHash = changed.TreeHash
	case "noop":
		view.Candidate.CurrentTree = mergeBefore
		view.Tree = mergeBefore
		view.Session.Candidate.TreeHash = mergeBefore.TreeHash
	}
	workspace := &patchUndoWorkspaceFake{view: view, now: mergeFixture.workspace.now.Add(time.Second)}
	source := &patchUndoSourceFake{merge: merged.Plan, application: *merged.Application}
	store := &patchUndoStoreFake{}
	service, err := NewPatchUndoService(
		source, &patchUndoTreeFake{tree: mergeBefore}, workspace, store,
		&agentAccessFake{}, func() time.Time { return mergeFixture.workspace.now.Add(time.Second) },
	)
	if err != nil {
		t.Fatal(err)
	}
	return patchUndoServiceFixture{
		service: service, workspace: workspace,
		input: UndoPatchInput{
			MergeID: merged.Plan.ID, ExpectedMergeContentHash: merged.Plan.ContentHash,
			ActorID: merged.Plan.CreatedBy, OperationID: "undo-request-1",
			ExpectedSessionVersion:   view.Session.Version,
			ExpectedSessionEpoch:     view.Session.SessionEpoch,
			ExpectedCandidateVersion: view.Candidate.Version,
			ExpectedWriterLeaseEpoch: view.Candidate.WriterLeaseEpoch,
		},
	}
}

func jsonContainsNullArray(raw []byte, key string) bool {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	return string(value[key]) == "null"
}
