package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *GORMStore) CreateSummaryCheckpoint(
	ctx context.Context,
	projectID, conversationID, actorID uuid.UUID,
	expectedConversationETag string,
	input CreateSummaryCheckpointInput,
) (ConversationSummaryCheckpoint, error) {
	input, err := normalizeSummaryCheckpointInput(input)
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	throughMessageID, _ := uuid.Parse(input.ThroughMessageID)
	var checkpoint storage.ConversationSummaryCheckpointModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		conversation, err := lockActiveConversation(transaction, projectID, conversationID)
		if err != nil {
			return err
		}
		if ConversationETag(conversation.ID.String(), conversation.Version) != expectedConversationETag {
			return core.ErrConflict
		}
		previous, err := loadCheckpointHead(transaction, conversation)
		if err != nil {
			return err
		}
		var through storage.ConversationMessageModel
		if err := transaction.Where("id = ? AND conversation_id = ?", throughMessageID, conversationID).Take(&through).Error; err != nil {
			return mapNotFound(err)
		}
		prefixHash, messageCount, contentBytes, err := computeCheckpointPrefix(transaction, conversationID, previous, through)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		checkpoint = storage.ConversationSummaryCheckpointModel{
			ID: uuid.New(), ProjectID: projectID, ConversationID: conversationID,
			ThroughMessageID: through.ID, ThroughSequence: through.Sequence,
			MessageCount: messageCount, ContentBytes: contentBytes,
			PrefixHash: prefixHash, HashAlgorithm: ConversationPrefixHashAlgorithm,
			Summary: input.Summary, SummaryHash: sha256Bytes([]byte(input.Summary)),
			Status: string(SummaryCheckpointPendingReview), Version: 1,
			CreatedBy: actorID, CreatedAt: now, ReviewReason: "",
		}
		if previous != nil {
			previousID := previous.ID
			checkpoint.PreviousCheckpointID = &previousID
		}
		if err := transaction.Create(&checkpoint).Error; err != nil {
			if uniqueViolation(err) {
				return core.ErrConflict
			}
			return err
		}
		if err := conversationAudit(transaction, projectID, actorID, "conversation.summary_checkpoint.created", "conversation_summary_checkpoint", checkpoint.ID.String(), map[string]any{
			"conversationId": conversationID.String(), "throughMessageId": through.ID.String(),
			"throughSequence": through.Sequence, "previousCheckpointId": uuidString(checkpoint.PreviousCheckpointID),
			"prefixHash": sha256Ref(checkpoint.PrefixHash),
		}); err != nil {
			return err
		}
		return conversationOutbox(transaction, "conversation_summary_checkpoint", checkpoint.ID.String(), "conversation.summary_checkpoint.created", map[string]any{
			"projectId": projectID.String(), "conversationId": conversationID.String(),
			"checkpointId": checkpoint.ID.String(), "throughSequence": through.Sequence,
		})
	})
	if err != nil {
		return ConversationSummaryCheckpoint{}, fmt.Errorf("create conversation summary checkpoint: %w", err)
	}
	return summaryCheckpointFromModel(checkpoint), nil
}

func (s *GORMStore) GetSummaryCheckpoint(
	ctx context.Context,
	projectID, conversationID, checkpointID uuid.UUID,
) (ConversationSummaryCheckpoint, error) {
	var model storage.ConversationSummaryCheckpointModel
	err := s.database.WithContext(ctx).
		Where("id = ? AND project_id = ? AND conversation_id = ?", checkpointID, projectID, conversationID).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ConversationSummaryCheckpoint{}, core.ErrNotFound
	}
	if err != nil {
		return ConversationSummaryCheckpoint{}, fmt.Errorf("load conversation summary checkpoint: %w", err)
	}
	return summaryCheckpointFromModel(model), nil
}

func (s *GORMStore) ListSummaryCheckpoints(
	ctx context.Context,
	projectID, conversationID uuid.UUID,
	options ListOptions,
) (SummaryCheckpointPage, error) {
	if err := s.ensureConversation(ctx, projectID, conversationID); err != nil {
		return SummaryCheckpointPage{}, err
	}
	query := s.database.WithContext(ctx).
		Where("project_id = ? AND conversation_id = ?", projectID, conversationID)
	if options.Cursor != "" {
		createdAt, id, err := decodeHistoryCursor(options.Cursor)
		if err != nil {
			return SummaryCheckpointPage{}, err
		}
		query = query.Where("(created_at, id) < (?, ?)", createdAt, id)
	}
	var models []storage.ConversationSummaryCheckpointModel
	if err := query.Order("created_at DESC, id DESC").Limit(options.Limit + 1).Find(&models).Error; err != nil {
		return SummaryCheckpointPage{}, fmt.Errorf("list conversation summary checkpoints: %w", err)
	}
	page := SummaryCheckpointPage{Items: make([]ConversationSummaryCheckpoint, 0, min(len(models), options.Limit))}
	for index, model := range models {
		if index == options.Limit {
			last := models[index-1]
			page.NextCursor = encodeHistoryCursor(last.CreatedAt, last.ID)
			break
		}
		page.Items = append(page.Items, summaryCheckpointFromModel(model))
	}
	return page, nil
}

func (s *GORMStore) ListSummaryCheckpointSourceMessages(
	ctx context.Context,
	projectID, conversationID, checkpointID uuid.UUID,
	options ListOptions,
) (MessagePage, error) {
	var checkpoint storage.ConversationSummaryCheckpointModel
	if err := s.database.WithContext(ctx).
		Where("id = ? AND project_id = ? AND conversation_id = ?", checkpointID, projectID, conversationID).
		Take(&checkpoint).Error; err != nil {
		return MessagePage{}, mapNotFound(err)
	}
	startSequence := uint64(1)
	if checkpoint.PreviousCheckpointID != nil {
		var previous storage.ConversationSummaryCheckpointModel
		if err := s.database.WithContext(ctx).
			Where("id = ? AND conversation_id = ?", *checkpoint.PreviousCheckpointID, conversationID).
			Take(&previous).Error; err != nil {
			return MessagePage{}, mapNotFound(err)
		}
		startSequence = previous.ThroughSequence + 1
	}
	query := s.database.WithContext(ctx).
		Where("conversation_id = ? AND sequence >= ? AND sequence <= ?", conversationID, startSequence, checkpoint.ThroughSequence)
	if options.Cursor != "" {
		sequence, err := decodeMessageCursor(options.Cursor)
		if err != nil || sequence < startSequence || sequence >= checkpoint.ThroughSequence {
			return MessagePage{}, fmt.Errorf("%w: checkpoint source cursor", core.ErrInvalidInput)
		}
		query = query.Where("sequence > ?", sequence)
	}
	var models []storage.ConversationMessageModel
	if err := query.Order("sequence ASC, id ASC").Limit(options.Limit + 1).Find(&models).Error; err != nil {
		return MessagePage{}, fmt.Errorf("list checkpoint source messages: %w", err)
	}
	page := MessagePage{Items: make([]Message, 0, min(len(models), options.Limit))}
	for index, model := range models {
		if index == options.Limit {
			page.NextCursor = encodeMessageCursor(models[index-1].Sequence)
			break
		}
		page.Items = append(page.Items, messageFromModel(model))
	}
	return page, nil
}

func (s *GORMStore) DecideSummaryCheckpoint(
	ctx context.Context,
	projectID, conversationID, checkpointID, actorID uuid.UUID,
	expectedETag string,
	input DecideSummaryCheckpointInput,
) (ConversationSummaryCheckpoint, error) {
	input, err := normalizeSummaryCheckpointDecision(input)
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	var checkpoint storage.ConversationSummaryCheckpointModel
	soloSelfReview := false
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		conversation, err := lockActiveConversation(transaction, projectID, conversationID)
		if err != nil {
			return err
		}
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ? AND conversation_id = ?", checkpointID, projectID, conversationID).
			Take(&checkpoint).Error; err != nil {
			return mapNotFound(err)
		}
		if SummaryCheckpointETag(checkpoint.ID.String(), checkpoint.Version) != expectedETag {
			return core.ErrConflict
		}
		if checkpoint.Status != string(SummaryCheckpointPendingReview) {
			return core.ErrConflict
		}
		if checkpoint.CreatedBy == actorID {
			governance, err := core.LockProjectGovernance(ctx, transaction, projectID)
			if err != nil {
				return err
			}
			var member storage.ProjectMemberModel
			if err := transaction.Where("project_id = ? AND user_id = ?", projectID, actorID).Take(&member).Error; err != nil {
				return mapNotFound(err)
			}
			if err := core.RequireSoloSelfReview(governance, core.Role(member.Role), input.SoloReviewConfirmed, input.Reason); err != nil {
				return err
			}
			soloSelfReview = true
		}
		now := s.now().UTC()
		status := SummaryCheckpointRejected
		if input.Decision == SummaryCheckpointApprove {
			status = SummaryCheckpointApproved
			if !sameOptionalUUID(checkpoint.PreviousCheckpointID, conversation.SummaryCheckpointHeadID) {
				return ErrSummaryCheckpointChainStale
			}
			previous, err := loadCheckpointByOptionalID(transaction, conversationID, checkpoint.PreviousCheckpointID)
			if err != nil {
				return err
			}
			var through storage.ConversationMessageModel
			if err := transaction.Where("id = ? AND conversation_id = ?", checkpoint.ThroughMessageID, conversationID).Take(&through).Error; err != nil {
				return mapNotFound(err)
			}
			prefixHash, messageCount, contentBytes, err := computeCheckpointPrefix(transaction, conversationID, previous, through)
			if err != nil {
				return err
			}
			if !equalBytes(prefixHash, checkpoint.PrefixHash) || messageCount != checkpoint.MessageCount ||
				contentBytes != checkpoint.ContentBytes || !equalBytes(sha256Bytes([]byte(checkpoint.Summary)), checkpoint.SummaryHash) {
				return fmt.Errorf("%w: checkpoint source binding changed", core.ErrConflict)
			}
		}
		result := transaction.Model(&storage.ConversationSummaryCheckpointModel{}).
			Where("id = ? AND version = ? AND status = ?", checkpoint.ID, checkpoint.Version, SummaryCheckpointPendingReview).
			Updates(map[string]any{
				"status": status, "version": checkpoint.Version + 1,
				"reviewed_by": actorID, "reviewed_at": now, "review_reason": input.Reason,
			})
		if result.Error != nil {
			if uniqueViolation(result.Error) {
				return ErrSummaryCheckpointChainStale
			}
			return result.Error
		}
		if result.RowsAffected != 1 {
			return core.ErrConflict
		}
		checkpoint.Status = string(status)
		checkpoint.Version++
		checkpoint.ReviewedBy = &actorID
		checkpoint.ReviewedAt = &now
		checkpoint.ReviewReason = input.Reason
		if status == SummaryCheckpointApproved {
			update := transaction.Model(&storage.ConversationModel{}).
				Where("id = ? AND project_id = ? AND version = ?", conversation.ID, projectID, conversation.Version).
				Updates(map[string]any{
					"summary_checkpoint_head_id": checkpoint.ID,
					"version":                    conversation.Version + 1,
					"updated_at":                 now,
				})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return core.ErrConflict
			}
			siblings := transaction.Model(&storage.ConversationSummaryCheckpointModel{}).
				Where("conversation_id = ? AND id <> ? AND status = ?", conversationID, checkpoint.ID, SummaryCheckpointPendingReview)
			if checkpoint.PreviousCheckpointID == nil {
				siblings = siblings.Where("previous_checkpoint_id IS NULL")
			} else {
				siblings = siblings.Where("previous_checkpoint_id = ?", *checkpoint.PreviousCheckpointID)
			}
			if err := siblings.Updates(map[string]any{
				"status":        SummaryCheckpointSuperseded,
				"version":       gorm.Expr("version + 1"),
				"reviewed_at":   now,
				"review_reason": "Superseded by approved checkpoint " + checkpoint.ID.String(),
			}).Error; err != nil {
				return err
			}
		}
		auditMetadata := map[string]any{
			"conversationId": conversationID.String(), "decision": input.Decision, "status": status,
			"throughSequence": checkpoint.ThroughSequence, "prefixHash": sha256Ref(checkpoint.PrefixHash),
			"reason": input.Reason, "subjectAuthorId": checkpoint.CreatedBy.String(),
		}
		if soloSelfReview {
			auditMetadata["soloSelfReview"] = true
			auditMetadata["governanceMode"] = core.GovernanceModeSolo
		}
		if err := conversationAudit(transaction, projectID, actorID, "conversation.summary_checkpoint.decided", "conversation_summary_checkpoint", checkpoint.ID.String(), auditMetadata); err != nil {
			return err
		}
		return conversationOutbox(transaction, "conversation_summary_checkpoint", checkpoint.ID.String(), "conversation.summary_checkpoint.decided", map[string]any{
			"projectId": projectID.String(), "conversationId": conversationID.String(),
			"checkpointId": checkpoint.ID.String(), "decision": input.Decision, "status": status,
			"soloSelfReview": soloSelfReview,
		})
	})
	if err != nil {
		return ConversationSummaryCheckpoint{}, err
	}
	return summaryCheckpointFromModel(checkpoint), nil
}

func loadCheckpointHead(transaction *gorm.DB, conversation storage.ConversationModel) (*storage.ConversationSummaryCheckpointModel, error) {
	return loadCheckpointByOptionalID(transaction, conversation.ID, conversation.SummaryCheckpointHeadID)
}

func loadCheckpointByOptionalID(
	transaction *gorm.DB,
	conversationID uuid.UUID,
	checkpointID *uuid.UUID,
) (*storage.ConversationSummaryCheckpointModel, error) {
	if checkpointID == nil {
		return nil, nil
	}
	var checkpoint storage.ConversationSummaryCheckpointModel
	if err := transaction.Where("id = ? AND conversation_id = ? AND status = ?", *checkpointID, conversationID, SummaryCheckpointApproved).
		Take(&checkpoint).Error; err != nil {
		return nil, mapNotFound(err)
	}
	if checkpoint.HashAlgorithm != ConversationPrefixHashAlgorithm || len(checkpoint.PrefixHash) != 32 {
		return nil, fmt.Errorf("%w: invalid approved checkpoint binding", core.ErrConflict)
	}
	return &checkpoint, nil
}

func computeCheckpointPrefix(
	transaction *gorm.DB,
	conversationID uuid.UUID,
	previous *storage.ConversationSummaryCheckpointModel,
	through storage.ConversationMessageModel,
) ([]byte, uint64, uint64, error) {
	startSequence := uint64(1)
	prefixHash := conversationPrefixGenesis(conversationID)
	messageCount := uint64(0)
	contentBytes := uint64(0)
	if previous != nil {
		if previous.Status != string(SummaryCheckpointApproved) || previous.ThroughSequence >= through.Sequence ||
			previous.MessageCount != previous.ThroughSequence || previous.HashAlgorithm != ConversationPrefixHashAlgorithm ||
			len(previous.PrefixHash) != 32 {
			return nil, 0, 0, fmt.Errorf("%w: invalid checkpoint predecessor", core.ErrConflict)
		}
		startSequence = previous.ThroughSequence + 1
		prefixHash = append([]byte(nil), previous.PrefixHash...)
		messageCount = previous.MessageCount
		contentBytes = previous.ContentBytes
	}
	rows, err := transaction.Model(&storage.ConversationMessageModel{}).
		Where("conversation_id = ? AND sequence >= ? AND sequence <= ?", conversationID, startSequence, through.Sequence).
		Order("sequence ASC, id ASC").Rows()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("load checkpoint prefix: %w", err)
	}
	defer rows.Close()
	nextSequence := startSequence
	lastMessageID := uuid.Nil
	for rows.Next() {
		var model storage.ConversationMessageModel
		if err := transaction.ScanRows(rows, &model); err != nil {
			return nil, 0, 0, fmt.Errorf("scan checkpoint prefix: %w", err)
		}
		if model.ConversationID != conversationID || model.Sequence != nextSequence {
			return nil, 0, 0, fmt.Errorf("%w: checkpoint prefix is not continuous", core.ErrConflict)
		}
		prefixHash, err = advanceConversationPrefixHash(prefixHash, messageFromModel(model))
		if err != nil {
			return nil, 0, 0, err
		}
		messageCount++
		contentBytes += uint64(len(model.Content))
		nextSequence++
		lastMessageID = model.ID
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("scan checkpoint prefix: %w", err)
	}
	if nextSequence != through.Sequence+1 || lastMessageID != through.ID || messageCount != through.Sequence {
		return nil, 0, 0, fmt.Errorf("%w: checkpoint prefix changed while it was bound", core.ErrConflict)
	}
	return prefixHash, messageCount, contentBytes, nil
}

func summaryCheckpointFromModel(model storage.ConversationSummaryCheckpointModel) ConversationSummaryCheckpoint {
	return ConversationSummaryCheckpoint{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), ConversationID: model.ConversationID.String(),
		PreviousCheckpointID: uuidString(model.PreviousCheckpointID),
		ThroughMessageID:     model.ThroughMessageID.String(), ThroughSequence: model.ThroughSequence,
		MessageCount: model.MessageCount, ContentBytes: model.ContentBytes,
		PrefixHash: sha256Ref(model.PrefixHash), HashAlgorithm: model.HashAlgorithm,
		Summary: model.Summary, SummaryHash: sha256Ref(model.SummaryHash),
		Status: SummaryCheckpointStatus(model.Status), Version: model.Version,
		ETag:      SummaryCheckpointETag(model.ID.String(), model.Version),
		CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt,
		ReviewedBy: uuidString(model.ReviewedBy), ReviewedAt: model.ReviewedAt, ReviewReason: model.ReviewReason,
	}
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
