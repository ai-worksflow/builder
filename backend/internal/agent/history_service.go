package agent

import (
	"context"
	"errors"
	"fmt"
)

type PatchHistorySource interface {
	ResolveAttemptProject(context.Context, string) (string, error)
	GetAttempt(context.Context, string, string) (AgentAttempt, error)
}

type PatchHistoryStore interface {
	ListPatchMergePlans(context.Context, string, string, int) ([]PatchMergePlanRecord, error)
	GetPatchMergeApplication(context.Context, string, string) (PatchMergeApplication, bool, error)
	FindAppliedPatchUndoPlan(context.Context, string, string) (PatchUndoPlanRecord, bool, error)
	GetPatchUndoApplication(context.Context, string, string) (PatchUndoApplication, bool, error)
}

type PatchUndoHistoryItem struct {
	Plan        PatchUndoPlanRecord   `json:"plan"`
	Application *PatchUndoApplication `json:"application,omitempty"`
}

type PatchMergeHistoryItem struct {
	Plan        PatchMergePlanRecord   `json:"plan"`
	Application *PatchMergeApplication `json:"application,omitempty"`
	Undo        *PatchUndoHistoryItem  `json:"undo,omitempty"`
}

// PatchHistoryService exposes immutable merge receipts only after resolving
// an Attempt's project scope and authorizing project view. It never reads a
// cross-project plan, application, or undo payload before authorization.
type PatchHistoryService struct {
	source PatchHistorySource
	store  PatchHistoryStore
	access ProjectAuthorizer
}

func NewPatchHistoryService(
	source PatchHistorySource,
	store PatchHistoryStore,
	access ProjectAuthorizer,
) (*PatchHistoryService, error) {
	if source == nil || store == nil || access == nil {
		return nil, errors.New("Agent patch history source, store, and authorizer are required")
	}
	return &PatchHistoryService{source: source, store: store, access: access}, nil
}

func (service *PatchHistoryService) ListAttemptMerges(
	ctx context.Context,
	attemptID, actorID string,
	limit int,
) ([]PatchMergeHistoryItem, error) {
	if service == nil || ctx == nil || !validUUIDs(attemptID, actorID) || limit < 1 || limit > 100 {
		return nil, ErrPatchMergeInvalid
	}
	projectID, err := service.source.ResolveAttemptProject(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return nil, fmt.Errorf("authorize Agent patch history: %w", err)
	}
	attempt, err := service.source.GetAttempt(ctx, projectID, attemptID)
	if err != nil {
		return nil, err
	}
	if attempt.ID != attemptID || attempt.ProjectID != projectID {
		return nil, ErrAgentStoreIntegrity
	}
	plans, err := service.store.ListPatchMergePlans(ctx, projectID, attemptID, limit)
	if err != nil {
		return nil, err
	}
	items := make([]PatchMergeHistoryItem, len(plans))
	for index, plan := range plans {
		if plan.ProjectID != projectID || plan.AttemptID != attemptID {
			return nil, ErrAgentStoreIntegrity
		}
		item := PatchMergeHistoryItem{Plan: plan}
		application, applied, err := service.store.GetPatchMergeApplication(ctx, projectID, plan.ID)
		if err != nil {
			return nil, err
		}
		if applied {
			if application.MergeID != plan.ID || application.PlanContentHash != plan.ContentHash {
				return nil, ErrAgentStoreIntegrity
			}
			item.Application = &application
			undoPlan, undone, err := service.store.FindAppliedPatchUndoPlan(ctx, projectID, plan.ID)
			if err != nil {
				return nil, err
			}
			if undone {
				undoApplication, found, err := service.store.GetPatchUndoApplication(ctx, projectID, undoPlan.ID)
				if err != nil {
					return nil, err
				}
				if !found || undoPlan.MergeID != plan.ID ||
					undoApplication.UndoID != undoPlan.ID ||
					undoApplication.PlanContentHash != undoPlan.ContentHash {
					return nil, ErrAgentStoreIntegrity
				}
				item.Undo = &PatchUndoHistoryItem{Plan: undoPlan, Application: &undoApplication}
			}
		}
		items[index] = item
	}
	return items, nil
}

var _ PatchHistorySource = (*PostgresStore)(nil)
var _ PatchHistoryStore = (*PostgresStore)(nil)
