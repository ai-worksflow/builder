package conversation

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

type summaryCheckpointStore interface {
	CreateSummaryCheckpoint(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, CreateSummaryCheckpointInput) (ConversationSummaryCheckpoint, error)
	GetSummaryCheckpoint(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (ConversationSummaryCheckpoint, error)
	ListSummaryCheckpoints(context.Context, uuid.UUID, uuid.UUID, ListOptions) (SummaryCheckpointPage, error)
	ListSummaryCheckpointSourceMessages(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ListOptions) (MessagePage, error)
	DecideSummaryCheckpoint(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, DecideSummaryCheckpointInput) (ConversationSummaryCheckpoint, error)
}

func (s *Service) CreateSummaryCheckpoint(
	ctx context.Context,
	projectID, conversationID, actorID, expectedConversationETag string,
	input CreateSummaryCheckpointInput,
) (ConversationSummaryCheckpoint, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	if strings.TrimSpace(expectedConversationETag) == "" {
		return ConversationSummaryCheckpoint{}, fmt.Errorf("%w: conversation checkpoint precondition", core.ErrInvalidInput)
	}
	input, err = normalizeSummaryCheckpointInput(input)
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	store, err := s.checkpointStore()
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	return store.CreateSummaryCheckpoint(ctx, projectUUID, conversationUUID, actorUUID, expectedConversationETag, input)
}

func (s *Service) GetSummaryCheckpoint(
	ctx context.Context,
	projectID, conversationID, checkpointID, actorID string,
) (ConversationSummaryCheckpoint, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	checkpointUUID, err := parseID(checkpointID, "summary checkpoint id")
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	store, err := s.checkpointStore()
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	return store.GetSummaryCheckpoint(ctx, projectUUID, conversationUUID, checkpointUUID)
}

func (s *Service) ListSummaryCheckpoints(
	ctx context.Context,
	projectID, conversationID, actorID string,
	options ListOptions,
) (SummaryCheckpointPage, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return SummaryCheckpointPage{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return SummaryCheckpointPage{}, err
	}
	options, err = normalizeListOptions(options)
	if err != nil {
		return SummaryCheckpointPage{}, err
	}
	store, err := s.checkpointStore()
	if err != nil {
		return SummaryCheckpointPage{}, err
	}
	return store.ListSummaryCheckpoints(ctx, projectUUID, conversationUUID, options)
}

func (s *Service) ListSummaryCheckpointSourceMessages(
	ctx context.Context,
	projectID, conversationID, checkpointID, actorID string,
	options ListOptions,
) (MessagePage, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return MessagePage{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return MessagePage{}, err
	}
	checkpointUUID, err := parseID(checkpointID, "summary checkpoint id")
	if err != nil {
		return MessagePage{}, err
	}
	options, err = normalizeListOptions(options)
	if err != nil {
		return MessagePage{}, err
	}
	store, err := s.checkpointStore()
	if err != nil {
		return MessagePage{}, err
	}
	return store.ListSummaryCheckpointSourceMessages(ctx, projectUUID, conversationUUID, checkpointUUID, options)
}

func (s *Service) DecideSummaryCheckpoint(
	ctx context.Context,
	projectID, conversationID, checkpointID, actorID, expectedETag string,
	input DecideSummaryCheckpointInput,
) (ConversationSummaryCheckpoint, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionReview)
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	checkpointUUID, err := parseID(checkpointID, "summary checkpoint id")
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	if strings.TrimSpace(expectedETag) == "" {
		return ConversationSummaryCheckpoint{}, fmt.Errorf("%w: summary checkpoint precondition", core.ErrInvalidInput)
	}
	input, err = normalizeSummaryCheckpointDecision(input)
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	store, err := s.checkpointStore()
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	return store.DecideSummaryCheckpoint(ctx, projectUUID, conversationUUID, checkpointUUID, actorUUID, expectedETag, input)
}

func (s *Service) checkpointStore() (summaryCheckpointStore, error) {
	store, ok := s.store.(summaryCheckpointStore)
	if !ok {
		return nil, fmt.Errorf("conversation summary checkpoint store is unavailable")
	}
	return store, nil
}
