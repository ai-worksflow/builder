package agent

import (
	"context"
	"errors"
	"testing"
)

type patchHistoryFake struct {
	projectID        string
	attempt          AgentAttempt
	plans            []PatchMergePlanRecord
	mergeApplication *PatchMergeApplication
	undoPlan         *PatchUndoPlanRecord
	undoApplication  *PatchUndoApplication
	payloadReads     int
}

func (store *patchHistoryFake) ResolveAttemptProject(context.Context, string) (string, error) {
	return store.projectID, nil
}

func (store *patchHistoryFake) GetAttempt(context.Context, string, string) (AgentAttempt, error) {
	store.payloadReads++
	return store.attempt, nil
}

func (store *patchHistoryFake) ListPatchMergePlans(
	context.Context, string, string, int,
) ([]PatchMergePlanRecord, error) {
	store.payloadReads++
	return append([]PatchMergePlanRecord(nil), store.plans...), nil
}

func (store *patchHistoryFake) GetPatchMergeApplication(
	context.Context, string, string,
) (PatchMergeApplication, bool, error) {
	store.payloadReads++
	if store.mergeApplication == nil {
		return PatchMergeApplication{}, false, nil
	}
	return *store.mergeApplication, true, nil
}

func (store *patchHistoryFake) FindAppliedPatchUndoPlan(
	context.Context, string, string,
) (PatchUndoPlanRecord, bool, error) {
	store.payloadReads++
	if store.undoPlan == nil {
		return PatchUndoPlanRecord{}, false, nil
	}
	return *store.undoPlan, true, nil
}

func (store *patchHistoryFake) GetPatchUndoApplication(
	context.Context, string, string,
) (PatchUndoApplication, bool, error) {
	store.payloadReads++
	if store.undoApplication == nil {
		return PatchUndoApplication{}, false, nil
	}
	return *store.undoApplication, true, nil
}

func TestPatchHistoryAuthorizesBeforePayloadReads(t *testing.T) {
	fixture := newPatchMergeServiceFixture(t, false)
	source := fixture.service.source.(*patchMergeSourceFake)
	store := &patchHistoryFake{
		projectID: source.attempt.ProjectID,
		attempt:   source.attempt,
	}
	access := &agentAccessFake{err: errors.New("forbidden")}
	service, err := NewPatchHistoryService(store, store, access)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ListAttemptMerges(
		context.Background(), source.attempt.ID, fixture.input.ActorID, 50,
	)
	if err == nil || access.viewCalls != 1 || store.payloadReads != 0 {
		t.Fatalf("authorization err=%v viewCalls=%d payloadReads=%d", err, access.viewCalls, store.payloadReads)
	}
}

func TestPatchHistoryReturnsAppliedMergeAndUndoReceipts(t *testing.T) {
	fixture := newPatchUndoServiceFixture(t, "applied")
	undoResult, err := fixture.service.UndoPatch(context.Background(), fixture.input)
	if err != nil || undoResult.Application == nil {
		t.Fatalf("prepare undo: result=%#v err=%v", undoResult, err)
	}
	source := fixture.service.source.(*patchUndoSourceFake)
	store := &patchHistoryFake{
		projectID: source.merge.ProjectID,
		attempt: AgentAttempt{
			ID: source.merge.AttemptID, ProjectID: source.merge.ProjectID,
		},
		plans:            []PatchMergePlanRecord{source.merge},
		mergeApplication: &source.application,
		undoPlan:         &undoResult.Plan,
		undoApplication:  undoResult.Application,
	}
	access := &agentAccessFake{}
	service, err := NewPatchHistoryService(store, store, access)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.ListAttemptMerges(
		context.Background(), source.merge.AttemptID, source.merge.CreatedBy, 50,
	)
	if err != nil || len(items) != 1 || items[0].Application == nil ||
		items[0].Undo == nil || items[0].Undo.Application == nil ||
		items[0].Plan.ID != source.merge.ID || items[0].Undo.Plan.ID != undoResult.Plan.ID ||
		access.viewCalls != 1 {
		t.Fatalf("history=%#v err=%v viewCalls=%d", items, err, access.viewCalls)
	}
}

var _ PatchHistorySource = (*patchHistoryFake)(nil)
var _ PatchHistoryStore = (*patchHistoryFake)(nil)
