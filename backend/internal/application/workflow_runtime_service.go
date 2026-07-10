package application

import (
	"context"
	"encoding/json"

	"github.com/worksflow/builder/backend/internal/domain"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
)

// WorkflowRuntimeService is the application boundary for the production
// lease/CAS engine. Transport code does not need to manipulate aggregates.
type WorkflowRuntimeService struct {
	Engine *runtime.Engine
}

func (s WorkflowRuntimeService) Start(ctx context.Context, request runtime.StartRequest) (*runtime.RunRecord, error) {
	if s.Engine == nil {
		return nil, domain.ErrInvalidArgument
	}
	return s.Engine.Start(ctx, request)
}

func (s WorkflowRuntimeService) RunOne(ctx context.Context, workerID string) error {
	if s.Engine == nil {
		return domain.ErrInvalidArgument
	}
	return s.Engine.ClaimAndExecute(ctx, workerID)
}

func (s WorkflowRuntimeService) RecordProposal(ctx context.Context, runID, nodeKey string, proposal domain.ProposalRef, actorID string) error {
	if s.Engine == nil {
		return domain.ErrInvalidArgument
	}
	return s.Engine.RecordProposal(ctx, runID, nodeKey, proposal, actorID)
}

func (s WorkflowRuntimeService) SubmitHumanInput(ctx context.Context, runID, nodeKey string, output json.RawMessage, actorID string) error {
	if s.Engine == nil {
		return domain.ErrInvalidArgument
	}
	return s.Engine.SubmitHumanInput(ctx, runID, nodeKey, output, actorID)
}

func (s WorkflowRuntimeService) ResolveReview(ctx context.Context, runID, nodeKey, actorID string, resolution runtime.ReviewResolution, reason string) error {
	if s.Engine == nil {
		return domain.ErrInvalidArgument
	}
	return s.Engine.ResolveReview(ctx, runID, nodeKey, actorID, resolution, reason)
}

func (s WorkflowRuntimeService) WaiveNode(ctx context.Context, runID, nodeKey, actorID, reason string) error {
	if s.Engine == nil {
		return domain.ErrInvalidArgument
	}
	return s.Engine.WaiveNode(ctx, runID, nodeKey, actorID, reason)
}

func (s WorkflowRuntimeService) Cancel(ctx context.Context, runID, actorID, reason string) error {
	if s.Engine == nil {
		return domain.ErrInvalidArgument
	}
	return s.Engine.Cancel(ctx, runID, actorID, reason)
}
