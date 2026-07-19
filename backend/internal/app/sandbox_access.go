package app

import (
	"context"
	"errors"
	"strings"

	"github.com/worksflow/builder/backend/internal/agent"
	"github.com/worksflow/builder/backend/internal/core"
)

func sandboxRunnerDigest(image string) string {
	index := strings.LastIndex(image, "@sha256:")
	if index < 0 {
		return ""
	}
	return image[index+1:]
}

// sandboxAccessAdapter keeps repository/sandbox packages independent from the
// platform role model while ensuring every façade operation uses the same
// authoritative project membership checks as the rest of the API.
type sandboxAccessAdapter struct {
	access *core.AccessControl
}

func (adapter sandboxAccessAdapter) RequireProjectView(ctx context.Context, projectID, actorID string) error {
	_, err := adapter.access.Authorize(ctx, projectID, actorID, core.ActionView)
	return err
}

func (adapter sandboxAccessAdapter) RequireProjectEdit(ctx context.Context, projectID, actorID string) error {
	_, err := adapter.access.Authorize(ctx, projectID, actorID, core.ActionEdit)
	return err
}

func (adapter sandboxAccessAdapter) RequireSandboxControl(ctx context.Context, projectID, actorID string) error {
	_, err := adapter.access.Authorize(ctx, projectID, actorID, core.ActionEdit)
	return err
}

func (adapter sandboxAccessAdapter) AuthorizeAgentWorker(
	ctx context.Context,
	principal agent.WorkerPrincipal,
	attempt agent.AgentAttempt,
	action string,
) error {
	if principal.ActorID != attempt.CreatedBy || principal.WorkerID == "" {
		return errors.New("Agent worker principal does not own the immutable Attempt authority")
	}
	switch action {
	case "claim", "renew", "advance", "mark_stale":
	default:
		return errors.New("Agent worker action is not qualified")
	}
	_, err := adapter.access.Authorize(ctx, attempt.ProjectID, principal.ActorID, core.ActionEdit)
	return err
}
