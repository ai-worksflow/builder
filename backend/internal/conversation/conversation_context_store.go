package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type conversationMessageBudget struct {
	MessageCount int64 `gorm:"column:message_count"`
	ContentBytes int64 `gorm:"column:content_bytes"`
}

func (s *GORMStore) loadIntentConversationContext(
	ctx context.Context,
	conversationID uuid.UUID,
	trigger storage.ConversationMessageModel,
) (ProviderConversationContext, ConversationContextProvenance, error) {
	database := s.database.WithContext(ctx)
	var checkpoint storage.ConversationSummaryCheckpointModel
	checkpointQuery := database.
		Where("conversation_id = ? AND status = ? AND through_sequence < ?", conversationID, SummaryCheckpointApproved, trigger.Sequence).
		Order("through_sequence DESC").Limit(1).Take(&checkpoint)
	hasCheckpoint := true
	if errors.Is(checkpointQuery.Error, gorm.ErrRecordNotFound) {
		hasCheckpoint = false
	} else if checkpointQuery.Error != nil {
		return ProviderConversationContext{}, ConversationContextProvenance{}, fmt.Errorf("load approved conversation summary checkpoint: %w", checkpointQuery.Error)
	}
	startSequence := uint64(1)
	var checkpointRef *ApprovedSummaryCheckpointRef
	if hasCheckpoint {
		if checkpoint.HashAlgorithm != ConversationPrefixHashAlgorithm || len(checkpoint.PrefixHash) != 32 ||
			len(checkpoint.SummaryHash) != 32 || !equalBytes(checkpoint.SummaryHash, sha256Bytes([]byte(checkpoint.Summary))) {
			return ProviderConversationContext{}, ConversationContextProvenance{}, fmt.Errorf("%w: approved checkpoint binding is invalid", core.ErrConflict)
		}
		startSequence = checkpoint.ThroughSequence + 1
		checkpointRef = &ApprovedSummaryCheckpointRef{
			ID: checkpoint.ID.String(), ThroughMessageID: checkpoint.ThroughMessageID.String(),
			ThroughSequence: checkpoint.ThroughSequence, PrefixHash: sha256Ref(checkpoint.PrefixHash),
			SummaryHash: sha256Ref(checkpoint.SummaryHash), Summary: checkpoint.Summary,
		}
	}
	var budget conversationMessageBudget
	if err := database.Table("conversation_messages").
		Select("COUNT(*) AS message_count, COALESCE(SUM(octet_length(content)), 0) AS content_bytes").
		Where("conversation_id = ? AND sequence >= ? AND sequence <= ?", conversationID, startSequence, trigger.Sequence).
		Scan(&budget).Error; err != nil {
		return ProviderConversationContext{}, ConversationContextProvenance{}, fmt.Errorf("measure conversation generation context: %w", err)
	}
	logicalMessages := uint64(budget.MessageCount)
	if checkpointRef != nil {
		logicalMessages++
	}
	var models []storage.ConversationMessageModel
	if err := database.
		Where("conversation_id = ? AND sequence >= ? AND sequence <= ?", conversationID, startSequence, trigger.Sequence).
		Order("sequence ASC, id ASC").Find(&models).Error; err != nil {
		return ProviderConversationContext{}, ConversationContextProvenance{}, fmt.Errorf("load conversation generation tail: %w", err)
	}
	if int64(len(models)) != budget.MessageCount || len(models) == 0 || models[len(models)-1].ID != trigger.ID {
		return ProviderConversationContext{}, ConversationContextProvenance{}, fmt.Errorf("%w: conversation message tail changed while generation context was loaded", core.ErrConflict)
	}
	messages := make([]Message, 0, len(models))
	bytesLoaded := uint64(0)
	for index, model := range models {
		expectedSequence := startSequence + uint64(index)
		if model.Sequence != expectedSequence {
			return ProviderConversationContext{}, ConversationContextProvenance{}, fmt.Errorf("%w: conversation message tail is not continuous", core.ErrConflict)
		}
		bytesLoaded += uint64(len(model.Content))
		messages = append(messages, messageFromModel(model))
	}
	if bytesLoaded != uint64(budget.ContentBytes) {
		return ProviderConversationContext{}, ConversationContextProvenance{}, fmt.Errorf("%w: conversation message tail changed while generation context was loaded", core.ErrConflict)
	}
	providerContext := ProviderConversationContext{ApprovedCheckpoint: checkpointRef, TailMessages: messages}
	contextBytes, err := platformdomain.CanonicalJSON(providerContext)
	if err != nil {
		return ProviderConversationContext{}, ConversationContextProvenance{}, err
	}
	if logicalMessages > maxIntentConversationMessages || len(contextBytes) > maxIntentConversationMessageBytes {
		required := &IntentSummaryCheckpointRequiredError{
			TriggerMessageID: trigger.ID.String(), TriggerSequence: trigger.Sequence,
			MessageCount: logicalMessages, MessageContentBytes: uint64(budget.ContentBytes),
			ContextBytes: uint64(len(contextBytes)),
		}
		if checkpointRef != nil {
			required.MessageContentBytes += uint64(len(checkpointRef.Summary))
			required.CurrentApprovedCheckpointID = checkpointRef.ID
			required.CurrentThroughSequence = checkpointRef.ThroughSequence
		}
		if trigger.Sequence > 1 {
			var recommended storage.ConversationMessageModel
			if err := database.Where("conversation_id = ? AND sequence = ?", conversationID, trigger.Sequence-1).
				Take(&recommended).Error; err != nil {
				return ProviderConversationContext{}, ConversationContextProvenance{}, fmt.Errorf("%w: checkpoint recommendation message is missing", core.ErrConflict)
			}
			required.RecommendedThroughMessageID = recommended.ID.String()
			required.RecommendedThroughSequence = recommended.Sequence
		}
		return ProviderConversationContext{}, ConversationContextProvenance{}, required
	}
	tailBytes, err := platformdomain.CanonicalJSON(messages)
	if err != nil {
		return ProviderConversationContext{}, ConversationContextProvenance{}, err
	}
	mode := "full_prefix"
	if checkpointRef != nil {
		mode = "checkpoint_tail"
	}
	provenance := ConversationContextProvenance{
		Version: 1, Mode: mode, TriggerMessageID: trigger.ID.String(), Checkpoint: checkpointRef,
		Tail: ConversationTailProvenance{
			FromSequence: startSequence, ToSequence: trigger.Sequence,
			MessageCount: uint64(len(messages)), ContentBytes: bytesLoaded,
			Hash: sha256Ref(sha256Bytes(tailBytes)),
		},
		ContextHash: sha256Ref(sha256Bytes(contextBytes)),
	}
	return providerContext, provenance, nil
}

func validateAndEncodeProposalConversationContext(
	transaction *gorm.DB,
	projectID, conversationID uuid.UUID,
	trigger storage.ConversationMessageModel,
	provenanceContext *ConversationContextProvenance,
	proposalProvenance ProposalProvenance,
) ([]byte, *uuid.UUID, []byte, error) {
	if proposalProvenance.Origin == ProposalOriginSubmitted {
		if provenanceContext != nil || proposalProvenance.AI != nil || proposalProvenance.providerInputHash != "" {
			return nil, nil, nil, fmt.Errorf("%w: submitted proposal conversation context is server managed", core.ErrInvalidInput)
		}
		encoded, err := platformdomain.CanonicalJSON(map[string]any{"version": 1, "mode": "submitted"})
		return encoded, nil, nil, err
	}
	if proposalProvenance.Origin != ProposalOriginAI || proposalProvenance.AI == nil || provenanceContext == nil {
		return nil, nil, nil, fmt.Errorf("%w: AI proposal requires immutable conversation context provenance", core.ErrInvalidInput)
	}
	contextValue := *provenanceContext
	if contextValue.Version != 1 || contextValue.TriggerMessageID != trigger.ID.String() ||
		(contextValue.Mode != "full_prefix" && contextValue.Mode != "checkpoint_tail") {
		return nil, nil, nil, fmt.Errorf("%w: invalid conversation context provenance", core.ErrInvalidInput)
	}
	providerInputHash, err := parseSHA256Ref(contextValue.ProviderInputHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: provider input hash", core.ErrInvalidInput)
	}
	trustedProviderInputHash, err := parseSHA256Ref(proposalProvenance.providerInputHash)
	if err != nil || !equalBytes(providerInputHash, trustedProviderInputHash) {
		return nil, nil, nil, fmt.Errorf("%w: provider input hash changed before proposal persistence", core.ErrConflict)
	}
	startSequence := uint64(1)
	var checkpointID *uuid.UUID
	var approvedRef *ApprovedSummaryCheckpointRef
	if contextValue.Mode == "checkpoint_tail" {
		if contextValue.Checkpoint == nil {
			return nil, nil, nil, fmt.Errorf("%w: missing approved summary checkpoint", core.ErrInvalidInput)
		}
		parsedCheckpointID, err := uuid.Parse(contextValue.Checkpoint.ID)
		if err != nil || parsedCheckpointID.String() != contextValue.Checkpoint.ID {
			return nil, nil, nil, fmt.Errorf("%w: summary checkpoint id", core.ErrInvalidInput)
		}
		var checkpoint storage.ConversationSummaryCheckpointModel
		if err := transaction.Where(
			"id = ? AND project_id = ? AND conversation_id = ? AND status = ?",
			parsedCheckpointID, projectID, conversationID, SummaryCheckpointApproved,
		).Take(&checkpoint).Error; err != nil {
			return nil, nil, nil, mapNotFound(err)
		}
		approvedRef = &ApprovedSummaryCheckpointRef{
			ID: checkpoint.ID.String(), ThroughMessageID: checkpoint.ThroughMessageID.String(),
			ThroughSequence: checkpoint.ThroughSequence, PrefixHash: sha256Ref(checkpoint.PrefixHash),
			SummaryHash: sha256Ref(checkpoint.SummaryHash), Summary: checkpoint.Summary,
		}
		if !sameApprovedSummaryCheckpointRef(contextValue.Checkpoint, approvedRef) || checkpoint.ThroughSequence >= trigger.Sequence {
			return nil, nil, nil, fmt.Errorf("%w: summary checkpoint provenance changed", core.ErrConflict)
		}
		startSequence = checkpoint.ThroughSequence + 1
		checkpointID = &parsedCheckpointID
	} else if contextValue.Checkpoint != nil {
		return nil, nil, nil, fmt.Errorf("%w: full-prefix context cannot reference a checkpoint", core.ErrInvalidInput)
	}
	if contextValue.Tail.FromSequence != startSequence || contextValue.Tail.ToSequence != trigger.Sequence ||
		contextValue.Tail.MessageCount != trigger.Sequence-startSequence+1 {
		return nil, nil, nil, fmt.Errorf("%w: conversation context tail range", core.ErrInvalidInput)
	}
	var models []storage.ConversationMessageModel
	if err := transaction.Where(
		"conversation_id = ? AND sequence >= ? AND sequence <= ?", conversationID, startSequence, trigger.Sequence,
	).Order("sequence ASC, id ASC").Find(&models).Error; err != nil {
		return nil, nil, nil, err
	}
	if uint64(len(models)) != contextValue.Tail.MessageCount || len(models) == 0 || models[len(models)-1].ID != trigger.ID {
		return nil, nil, nil, fmt.Errorf("%w: conversation context tail changed", core.ErrConflict)
	}
	messages := make([]Message, 0, len(models))
	contentBytes := uint64(0)
	for index, model := range models {
		if model.Sequence != startSequence+uint64(index) {
			return nil, nil, nil, fmt.Errorf("%w: conversation context tail is not continuous", core.ErrConflict)
		}
		contentBytes += uint64(len(model.Content))
		messages = append(messages, messageFromModel(model))
	}
	if contentBytes != contextValue.Tail.ContentBytes {
		return nil, nil, nil, fmt.Errorf("%w: conversation context tail byte count changed", core.ErrConflict)
	}
	tailBytes, err := platformdomain.CanonicalJSON(messages)
	if err != nil {
		return nil, nil, nil, err
	}
	if contextValue.Tail.Hash != sha256Ref(sha256Bytes(tailBytes)) {
		return nil, nil, nil, fmt.Errorf("%w: conversation context tail hash changed", core.ErrConflict)
	}
	providerContext := ProviderConversationContext{ApprovedCheckpoint: approvedRef, TailMessages: messages}
	contextBytes, err := platformdomain.CanonicalJSON(providerContext)
	if err != nil {
		return nil, nil, nil, err
	}
	if contextValue.ContextHash != sha256Ref(sha256Bytes(contextBytes)) {
		return nil, nil, nil, fmt.Errorf("%w: conversation context hash changed", core.ErrConflict)
	}
	encoded, err := platformdomain.CanonicalJSON(contextValue)
	if err != nil {
		return nil, nil, nil, err
	}
	return encoded, checkpointID, providerInputHash, nil
}

func sameApprovedSummaryCheckpointRef(left, right *ApprovedSummaryCheckpointRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ID == right.ID && left.ThroughMessageID == right.ThroughMessageID &&
		left.ThroughSequence == right.ThroughSequence && left.PrefixHash == right.PrefixHash &&
		left.SummaryHash == right.SummaryHash && left.Summary == right.Summary
}

func decodeStoredConversationContext(
	raw json.RawMessage,
	checkpointID *uuid.UUID,
	providerInputHash []byte,
) (*ConversationContextProvenance, error) {
	var value ConversationContextProvenance
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil, fmt.Errorf("decode stored conversation context")
	}
	switch value.Mode {
	case "legacy_unrecorded":
		if value.Version != 0 || checkpointID != nil || len(providerInputHash) != 0 {
			return nil, fmt.Errorf("invalid legacy conversation context")
		}
	case "submitted":
		if value.Version != 1 || checkpointID != nil || len(providerInputHash) != 0 {
			return nil, fmt.Errorf("invalid submitted conversation context")
		}
	case "full_prefix":
		hash, err := parseSHA256Ref(value.ProviderInputHash)
		if err != nil || value.Version != 1 || checkpointID != nil || !equalBytes(hash, providerInputHash) {
			return nil, fmt.Errorf("invalid full-prefix conversation context")
		}
	case "checkpoint_tail":
		hash, err := parseSHA256Ref(value.ProviderInputHash)
		if err != nil || value.Version != 1 || checkpointID == nil || value.Checkpoint == nil ||
			value.Checkpoint.ID != checkpointID.String() || !equalBytes(hash, providerInputHash) {
			return nil, fmt.Errorf("invalid checkpoint-tail conversation context")
		}
	default:
		return nil, fmt.Errorf("unknown stored conversation context")
	}
	return &value, nil
}

func sameConversationContext(left, right *ConversationContextProvenance) bool {
	leftJSON, leftErr := platformdomain.CanonicalJSON(left)
	rightJSON, rightErr := platformdomain.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && equalBytes(leftJSON, rightJSON)
}
