package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type commandClaim struct {
	Command ConversationCommand
	Token   uuid.UUID
}

type GORMStore struct {
	database *gorm.DB
	now      func() time.Time
}

type intentDefinitionContext struct {
	VersionID    string          `json:"versionId"`
	DefinitionID string          `json:"definitionId"`
	Key          string          `json:"key"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Content      json.RawMessage `json:"content"`
}

type intentWorkbenchTargetContext struct {
	DefinitionVersionID string `json:"definitionVersionId"`
	RunID               string `json:"runId"`
	RootBundleID        string `json:"rootBundleId"`
	ActiveBundleID      string `json:"activeBundleId"`
	ManifestGroup       string `json:"manifestGroup"`
	Ordinal             int    `json:"ordinal"`
}

type intentGenerationContext struct {
	Messages         []Message                      `json:"messages"`
	Definitions      []intentDefinitionContext      `json:"definitions"`
	WorkbenchTargets []intentWorkbenchTargetContext `json:"workbenchTargets"`
}

var activeWorkbenchRunStatuses = []string{"running", "waiting_input", "waiting_review"}

var executableImplementationProposalStatuses = []string{"open", "reviewing", "ready"}

const maxIntentWorkbenchTargets = 100

func NewGORMStore(database *gorm.DB) (*GORMStore, error) {
	if database == nil {
		return nil, errors.New("conversation database is required")
	}
	return &GORMStore{database: database, now: time.Now}, nil
}

func (s *GORMStore) CreateConversation(ctx context.Context, projectID, actorID uuid.UUID, title string) (Conversation, error) {
	now := s.now().UTC()
	model := storage.ConversationModel{
		ID: uuid.New(), ProjectID: projectID, Title: title, Status: string(ConversationActive),
		Version: 1, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if err := conversationAudit(transaction, projectID, actorID, "conversation.created", "conversation", model.ID.String(), nil); err != nil {
			return err
		}
		return conversationOutbox(transaction, "conversation", model.ID.String(), "conversation.created", map[string]any{
			"projectId": projectID.String(), "conversationId": model.ID.String(), "actorId": actorID.String(),
		})
	})
	if err != nil {
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return conversationFromModel(model), nil
}

func (s *GORMStore) GetConversation(ctx context.Context, projectID, conversationID uuid.UUID) (Conversation, error) {
	var model storage.ConversationModel
	err := s.database.WithContext(ctx).
		Where("id = ? AND project_id = ?", conversationID, projectID).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Conversation{}, core.ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("load conversation: %w", err)
	}
	return conversationFromModel(model), nil
}

func (s *GORMStore) ListConversations(ctx context.Context, projectID uuid.UUID, options ListOptions) (ConversationPage, error) {
	query := s.database.WithContext(ctx).Where("project_id = ?", projectID)
	if options.Cursor != "" {
		createdAt, id, err := decodeHistoryCursor(options.Cursor)
		if err != nil {
			return ConversationPage{}, err
		}
		query = query.Where("(created_at, id) < (?, ?)", createdAt, id)
	}
	var models []storage.ConversationModel
	if err := query.Order("created_at DESC, id DESC").Limit(options.Limit + 1).Find(&models).Error; err != nil {
		return ConversationPage{}, fmt.Errorf("list conversations: %w", err)
	}
	page := ConversationPage{Items: make([]Conversation, 0, min(len(models), options.Limit))}
	for index, model := range models {
		if index == options.Limit {
			last := models[index-1]
			page.NextCursor = encodeHistoryCursor(last.CreatedAt, last.ID)
			break
		}
		page.Items = append(page.Items, conversationFromModel(model))
	}
	return page, nil
}

func (s *GORMStore) UpdateConversation(ctx context.Context, projectID, conversationID, actorID uuid.UUID, expectedETag string, input UpdateConversationInput) (Conversation, error) {
	var model storage.ConversationModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ?", conversationID, projectID).Take(&model).Error; err != nil {
			return mapNotFound(err)
		}
		if ConversationETag(model.ID.String(), model.Version) != expectedETag {
			return core.ErrConflict
		}
		updates := map[string]any{"version": model.Version + 1, "updated_at": s.now().UTC()}
		if input.Title != nil {
			updates["title"] = *input.Title
			model.Title = *input.Title
		}
		if input.Status != nil {
			updates["status"] = string(*input.Status)
			model.Status = string(*input.Status)
			if *input.Status == ConversationArchived {
				archivedAt := s.now().UTC()
				updates["archived_at"] = archivedAt
				model.ArchivedAt = &archivedAt
			} else {
				updates["archived_at"] = nil
				model.ArchivedAt = nil
			}
		}
		result := transaction.Model(&storage.ConversationModel{}).
			Where("id = ? AND project_id = ? AND version = ?", conversationID, projectID, model.Version).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return core.ErrConflict
		}
		model.Version++
		model.UpdatedAt = updates["updated_at"].(time.Time)
		if err := conversationAudit(transaction, projectID, actorID, "conversation.updated", "conversation", conversationID.String(), map[string]any{"status": model.Status}); err != nil {
			return err
		}
		return conversationOutbox(transaction, "conversation", conversationID.String(), "conversation.updated", map[string]any{
			"projectId": projectID.String(), "conversationId": conversationID.String(), "version": model.Version,
		})
	})
	if err != nil {
		return Conversation{}, err
	}
	return conversationFromModel(model), nil
}

func (s *GORMStore) AppendUserMessage(ctx context.Context, projectID, conversationID, actorID uuid.UUID, content string) (Message, error) {
	var model storage.ConversationMessageModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		conversation, err := lockActiveConversation(transaction, projectID, conversationID)
		if err != nil {
			return err
		}
		sequence, err := nextMessageSequence(transaction, conversation.ID)
		if err != nil {
			return err
		}
		model = storage.ConversationMessageModel{
			ID: uuid.New(), ConversationID: conversation.ID, Sequence: sequence,
			Role: string(MessageUser), Content: content, CreatedBy: actorID, CreatedAt: s.now().UTC(),
		}
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if err := conversationAudit(transaction, projectID, actorID, "conversation.message.appended", "conversation_message", model.ID.String(), map[string]any{
			"conversationId": conversationID.String(), "sequence": sequence, "role": MessageUser,
		}); err != nil {
			return err
		}
		return conversationOutbox(transaction, "conversation", conversationID.String(), "conversation.message.appended", map[string]any{
			"projectId": projectID.String(), "conversationId": conversationID.String(), "messageId": model.ID.String(), "sequence": sequence,
		})
	})
	if err != nil {
		return Message{}, fmt.Errorf("append conversation message: %w", err)
	}
	return messageFromModel(model), nil
}

func (s *GORMStore) ListMessages(ctx context.Context, projectID, conversationID uuid.UUID, options ListOptions) (MessagePage, error) {
	if err := s.ensureConversation(ctx, projectID, conversationID); err != nil {
		return MessagePage{}, err
	}
	query := s.database.WithContext(ctx).Where("conversation_id = ?", conversationID)
	if options.Cursor != "" {
		sequence, err := decodeMessageCursor(options.Cursor)
		if err != nil {
			return MessagePage{}, err
		}
		query = query.Where("sequence > ?", sequence)
	}
	var models []storage.ConversationMessageModel
	if err := query.Order("sequence ASC, id ASC").Limit(options.Limit + 1).Find(&models).Error; err != nil {
		return MessagePage{}, fmt.Errorf("list conversation messages: %w", err)
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

func (s *GORMStore) CreateIntentProposal(ctx context.Context, projectID, conversationID, actorID uuid.UUID, input CreateIntentProposalInput, provenance ProposalProvenance, manifestJobType string) (WorkflowIntentProposal, Message, error) {
	var proposal storage.WorkflowIntentProposalModel
	var assistant storage.ConversationMessageModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		conversation, err := lockActiveConversation(transaction, projectID, conversationID)
		if err != nil {
			return err
		}
		triggerID, _ := uuid.Parse(input.TriggerMessageID)
		var trigger storage.ConversationMessageModel
		if err := transaction.Where("id = ? AND conversation_id = ? AND role = ?", triggerID, conversationID, MessageUser).Take(&trigger).Error; err != nil {
			return mapNotFound(err)
		}
		definitionVersionID, _ := uuid.Parse(input.SuggestedDefinitionVersionID)
		if err := validateDefinitionVersion(
			transaction, projectID, definitionVersionID, input.Kind == IntentStartWorkflow, manifestJobType,
		); err != nil {
			return err
		}
		if err := validateManifestIntent(transaction, projectID, input.ManifestIntent); err != nil {
			return err
		}
		if err := validateExactSourceRefs(transaction, projectID, input.SourceRefs); err != nil {
			return err
		}
		if input.Kind == IntentWorkbenchInstruction {
			if err := validateExpectedWorkbenchTarget(transaction, projectID, input); err != nil {
				return err
			}
		}
		sourceRefs, err := platformdomain.CanonicalJSON(input.SourceRefs)
		if err != nil {
			return err
		}
		manifestIntent, err := platformdomain.CanonicalJSON(input.ManifestIntent)
		if err != nil {
			return err
		}
		instruction, err := platformdomain.CanonicalJSON(input.WorkbenchInstruction)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		proposalID, assistantMessageID := uuid.New(), uuid.New()
		reviewedScope, err := reviewedConversationIntentScope(
			input.Scope,
			conversationID,
			trigger.ID,
			proposalID,
			assistantMessageID,
			input,
		)
		if err != nil {
			return err
		}
		var aiProvider, aiModel, aiResponseID *string
		if provenance.AI != nil {
			aiProvider = stringPointer(provenance.AI.Provider)
			aiModel = stringPointer(provenance.AI.Model)
			if provenance.AI.ResponseID != "" {
				aiResponseID = stringPointer(provenance.AI.ResponseID)
			}
		}
		proposal = storage.WorkflowIntentProposalModel{
			ID: proposalID, ProjectID: projectID, ConversationID: conversationID,
			TriggerMessageID: trigger.ID, AssistantMessageID: assistantMessageID,
			Kind: string(input.Kind), Status: string(ProposalPending), Version: 1,
			SuggestedDefinitionVersionID: definitionVersionID, Scope: reviewedScope,
			SourceRefs: sourceRefs, ManifestIntent: manifestIntent, WorkbenchInstruction: instruction,
			Origin: string(provenance.Origin), AIProvider: aiProvider, AIModel: aiModel, AIResponseID: aiResponseID,
			DecisionReason: "", ProposedBy: actorID, CreatedAt: now,
		}
		if err := transaction.Create(&proposal).Error; err != nil {
			return err
		}
		sequence, err := nextMessageSequence(transaction, conversation.ID)
		if err != nil {
			return err
		}
		assistant = storage.ConversationMessageModel{
			ID: assistantMessageID, ConversationID: conversationID, Sequence: sequence, Role: string(MessageAssistant),
			Content: input.AssistantContent, ProposalID: &proposal.ID, CreatedBy: actorID, CreatedAt: now,
		}
		if err := transaction.Create(&assistant).Error; err != nil {
			return err
		}
		auditMetadata := map[string]any{"conversationId": conversationID.String(), "kind": input.Kind, "origin": provenance.Origin}
		if provenance.AI != nil {
			auditMetadata["aiProvider"] = provenance.AI.Provider
			auditMetadata["aiModel"] = provenance.AI.Model
			auditMetadata["aiResponseId"] = provenance.AI.ResponseID
		}
		if err := conversationAudit(transaction, projectID, actorID, "conversation.intent_proposed", "workflow_intent_proposal", proposal.ID.String(), auditMetadata); err != nil {
			return err
		}
		return conversationOutbox(transaction, "workflow_intent_proposal", proposal.ID.String(), "conversation.intent_proposed", map[string]any{
			"projectId": projectID.String(), "conversationId": conversationID.String(), "proposalId": proposal.ID.String(), "kind": input.Kind, "origin": provenance.Origin,
		})
	})
	if err != nil {
		return WorkflowIntentProposal{}, Message{}, fmt.Errorf("create workflow intent proposal: %w", err)
	}
	result, err := proposalFromModel(proposal)
	if err != nil {
		return WorkflowIntentProposal{}, Message{}, err
	}
	return result, messageFromModel(assistant), nil
}

func reviewedConversationIntentScope(
	base json.RawMessage,
	conversationID uuid.UUID,
	triggerMessageID uuid.UUID,
	proposalID uuid.UUID,
	assistantMessageID uuid.UUID,
	input CreateIntentProposalInput,
) (json.RawMessage, error) {
	var scope map[string]any
	if err := json.Unmarshal(base, &scope); err != nil || scope == nil {
		return nil, fmt.Errorf("reviewed conversation scope must be an object")
	}
	if _, exists := scope["conversationIntent"]; exists {
		return nil, fmt.Errorf("reviewed conversation intent is server managed")
	}
	scope["conversationIntent"] = ReviewedConversationIntent{
		ConversationID: conversationID.String(), TriggerMessageID: triggerMessageID.String(),
		ProposalID: proposalID.String(), AssistantMessageID: assistantMessageID.String(),
		Kind: input.Kind, DefinitionVersionID: input.SuggestedDefinitionVersionID,
		WorkbenchInstruction: input.WorkbenchInstruction, ManifestIntent: input.ManifestIntent,
		SourceRefs: append([]platformdomain.ArtifactRef(nil), input.SourceRefs...),
	}
	canonical, err := platformdomain.CanonicalJSON(scope)
	if err != nil {
		return nil, fmt.Errorf("canonicalize reviewed conversation scope: %w", err)
	}
	if len(canonical) > 65536 {
		return nil, fmt.Errorf("reviewed conversation scope exceeds size limit")
	}
	return canonical, nil
}

func (s *GORMStore) IntentGenerationContext(
	ctx context.Context,
	projectID, conversationID, triggerMessageID uuid.UUID,
	candidateVersionIDs []uuid.UUID,
	sourceRefs []platformdomain.ArtifactRef,
	manifestIntent ManifestIntent,
	manifestJobType string,
) (intentGenerationContext, error) {
	if len(candidateVersionIDs) == 0 || len(candidateVersionIDs) > 20 {
		return intentGenerationContext{}, fmt.Errorf("%w: candidate definition versions", core.ErrInvalidInput)
	}
	if err := s.ensureActiveConversation(ctx, projectID, conversationID); err != nil {
		return intentGenerationContext{}, err
	}
	var trigger storage.ConversationMessageModel
	if err := s.database.WithContext(ctx).
		Where("id = ? AND conversation_id = ? AND role = ?", triggerMessageID, conversationID, MessageUser).
		Take(&trigger).Error; err != nil {
		return intentGenerationContext{}, mapNotFound(err)
	}
	if err := validateManifestIntent(s.database.WithContext(ctx), projectID, manifestIntent); err != nil {
		return intentGenerationContext{}, err
	}
	if err := validateExactSourceRefs(s.database.WithContext(ctx), projectID, sourceRefs); err != nil {
		return intentGenerationContext{}, err
	}
	var messageModels []storage.ConversationMessageModel
	if err := s.database.WithContext(ctx).Where("conversation_id = ? AND sequence <= ?", conversationID, trigger.Sequence).
		Order("sequence DESC").Limit(50).Find(&messageModels).Error; err != nil {
		return intentGenerationContext{}, fmt.Errorf("load conversation generation context: %w", err)
	}
	selectedMessages := make([]storage.ConversationMessageModel, 0, len(messageModels))
	bytesUsed := 0
	for _, model := range messageModels {
		if bytesUsed+len(model.Content) > 128<<10 && model.ID != triggerMessageID {
			continue
		}
		bytesUsed += len(model.Content)
		selectedMessages = append(selectedMessages, model)
	}
	messages := make([]Message, 0, len(selectedMessages))
	for index := len(selectedMessages) - 1; index >= 0; index-- {
		messages = append(messages, messageFromModel(selectedMessages[index]))
	}
	type definitionRow struct {
		ID           uuid.UUID
		DefinitionID uuid.UUID
		WorkflowKey  string
		Title        string
		Description  string
		Content      json.RawMessage
	}
	var rows []definitionRow
	if err := s.database.WithContext(ctx).Table("workflow_definition_versions AS version").
		Select("version.id, version.definition_id, definition.workflow_key, definition.title, definition.description, version.content").
		Joins("JOIN workflow_definitions AS definition ON definition.id = version.definition_id").
		Where("version.id IN ? AND version.published = TRUE AND (definition.project_id IS NULL OR definition.project_id = ?)", candidateVersionIDs, projectID).
		Order("version.id ASC").Scan(&rows).Error; err != nil {
		return intentGenerationContext{}, fmt.Errorf("load candidate workflow versions: %w", err)
	}
	if len(rows) != len(candidateVersionIDs) {
		return intentGenerationContext{}, core.ErrNotFound
	}
	for _, row := range rows {
		if err := validateStartDefinitionCompatibility(manifestJobType, row.Content); err != nil {
			return intentGenerationContext{}, err
		}
	}
	definitionRows := make(map[uuid.UUID]definitionRow, len(rows))
	for _, row := range rows {
		definitionRows[row.ID] = row
	}
	type workbenchTargetRow struct {
		DefinitionVersionID uuid.UUID
		RunID               uuid.UUID
		RootBundleID        uuid.UUID
		ActiveBundleID      uuid.UUID
		ManifestGroup       string
		Ordinal             int
	}
	var targetRows []workbenchTargetRow
	// Continuation targets are project runtime state, not start candidates.
	// Enumerate every authoritative active Workbench leaf in the tenant even
	// when its historical definition is incompatible with the current M1
	// governance manifest. Compatibility still applies to rows above only.
	if err := s.database.WithContext(ctx).Table("application_build_manifests AS leaf").
		Select(`run.definition_version_id AS definition_version_id,
			run.id AS run_id,
			root.id AS root_bundle_id,
			leaf.id AS active_bundle_id,
			leaf.manifest_group_key AS manifest_group,
			leaf.root_ordinal AS ordinal`).
		Joins("JOIN application_build_manifests AS root ON root.id = leaf.root_manifest_id AND root.project_id = leaf.project_id").
		Joins("JOIN workflow_runs AS run ON run.id = leaf.workflow_run_id AND run.project_id = leaf.project_id").
		Joins("JOIN workflow_definition_versions AS run_version ON run_version.id = run.definition_version_id").
		Joins("JOIN workflow_definitions AS definition ON definition.id = run_version.definition_id").
		Where("leaf.project_id = ? AND run.status IN ?", projectID, activeWorkbenchRunStatuses).
		Where("definition.project_id IS NULL OR definition.project_id = ?", projectID).
		Where("leaf.status = ? AND leaf.invalidated_at IS NULL", "frozen").
		Where("leaf.manifest_group_key IS NOT NULL AND leaf.root_ordinal IS NOT NULL").
		Where("root.root_manifest_id = root.id AND root.derived_from_id IS NULL").
		Where("root.workflow_run_id = leaf.workflow_run_id AND root.manifest_group_key = leaf.manifest_group_key AND root.root_ordinal = leaf.root_ordinal").
		Where("NOT EXISTS (?)", s.database.Table("application_build_manifests AS child").Select("1").Where("child.derived_from_id = leaf.id")).
		Order("run.id ASC, leaf.manifest_group_key ASC, leaf.root_ordinal ASC, root.id ASC, leaf.id ASC").
		Limit(maxIntentWorkbenchTargets).
		Scan(&targetRows).Error; err != nil {
		return intentGenerationContext{}, fmt.Errorf("load active workflow workbench targets: %w", err)
	}

	missingVersionIDs := make([]uuid.UUID, 0)
	seenMissingVersionIDs := make(map[uuid.UUID]struct{})
	for _, target := range targetRows {
		if _, exists := definitionRows[target.DefinitionVersionID]; exists {
			continue
		}
		if _, exists := seenMissingVersionIDs[target.DefinitionVersionID]; exists {
			continue
		}
		seenMissingVersionIDs[target.DefinitionVersionID] = struct{}{}
		missingVersionIDs = append(missingVersionIDs, target.DefinitionVersionID)
	}
	if len(missingVersionIDs) != 0 {
		var targetDefinitionRows []definitionRow
		if err := s.database.WithContext(ctx).Table("workflow_definition_versions AS version").
			Select("version.id, version.definition_id, definition.workflow_key, definition.title, definition.description, version.content").
			Joins("JOIN workflow_definitions AS definition ON definition.id = version.definition_id").
			Where("version.id IN ?", missingVersionIDs).
			Where("definition.project_id IS NULL OR definition.project_id = ?", projectID).
			Order("version.id ASC").Scan(&targetDefinitionRows).Error; err != nil {
			return intentGenerationContext{}, fmt.Errorf("load active target workflow versions: %w", err)
		}
		if len(targetDefinitionRows) != len(missingVersionIDs) {
			return intentGenerationContext{}, core.ErrConflict
		}
		for _, row := range targetDefinitionRows {
			definitionRows[row.ID] = row
		}
	}

	versionIDs := make([]uuid.UUID, 0, len(definitionRows))
	for versionID := range definitionRows {
		versionIDs = append(versionIDs, versionID)
	}
	sort.Slice(versionIDs, func(i, j int) bool { return versionIDs[i].String() < versionIDs[j].String() })
	definitions := make([]intentDefinitionContext, 0, len(versionIDs))
	for _, versionID := range versionIDs {
		row := definitionRows[versionID]
		definitions = append(definitions, intentDefinitionContext{
			VersionID: row.ID.String(), DefinitionID: row.DefinitionID.String(), Key: row.WorkflowKey,
			Title: row.Title, Description: row.Description, Content: append(json.RawMessage(nil), row.Content...),
		})
	}
	targets := make([]intentWorkbenchTargetContext, 0, len(targetRows))
	for _, row := range targetRows {
		targets = append(targets, intentWorkbenchTargetContext{
			DefinitionVersionID: row.DefinitionVersionID.String(), RunID: row.RunID.String(),
			RootBundleID: row.RootBundleID.String(), ActiveBundleID: row.ActiveBundleID.String(),
			ManifestGroup: row.ManifestGroup, Ordinal: row.Ordinal,
		})
	}
	return intentGenerationContext{Messages: messages, Definitions: definitions, WorkbenchTargets: targets}, nil
}

func (s *GORMStore) GetProposal(ctx context.Context, projectID, conversationID, proposalID uuid.UUID) (WorkflowIntentProposal, error) {
	var model storage.WorkflowIntentProposalModel
	err := s.database.WithContext(ctx).Where("id = ? AND project_id = ? AND conversation_id = ?", proposalID, projectID, conversationID).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return WorkflowIntentProposal{}, core.ErrNotFound
	}
	if err != nil {
		return WorkflowIntentProposal{}, fmt.Errorf("load workflow intent proposal: %w", err)
	}
	return proposalFromModel(model)
}

func (s *GORMStore) ListProposals(ctx context.Context, projectID, conversationID uuid.UUID, options ListOptions) (ProposalPage, error) {
	if err := s.ensureConversation(ctx, projectID, conversationID); err != nil {
		return ProposalPage{}, err
	}
	query := s.database.WithContext(ctx).Where("project_id = ? AND conversation_id = ?", projectID, conversationID)
	if options.Cursor != "" {
		createdAt, id, err := decodeHistoryCursor(options.Cursor)
		if err != nil {
			return ProposalPage{}, err
		}
		query = query.Where("(created_at, id) < (?, ?)", createdAt, id)
	}
	var models []storage.WorkflowIntentProposalModel
	if err := query.Order("created_at DESC, id DESC").Limit(options.Limit + 1).Find(&models).Error; err != nil {
		return ProposalPage{}, fmt.Errorf("list workflow intent proposals: %w", err)
	}
	page := ProposalPage{Items: make([]WorkflowIntentProposal, 0, min(len(models), options.Limit))}
	for index, model := range models {
		if index == options.Limit {
			last := models[index-1]
			page.NextCursor = encodeHistoryCursor(last.CreatedAt, last.ID)
			break
		}
		proposal, err := proposalFromModel(model)
		if err != nil {
			return ProposalPage{}, err
		}
		page.Items = append(page.Items, proposal)
	}
	return page, nil
}

func (s *GORMStore) DecideProposal(ctx context.Context, projectID, conversationID, proposalID, actorID uuid.UUID, expectedETag string, input DecideProposalInput) (WorkflowIntentProposal, *ConversationCommand, error) {
	var proposal storage.WorkflowIntentProposalModel
	var command *storage.ConversationCommandModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ? AND conversation_id = ?", proposalID, projectID, conversationID).Take(&proposal).Error; err != nil {
			return mapNotFound(err)
		}
		if ProposalETag(proposal.ID.String(), proposal.Version) != expectedETag {
			return core.ErrConflict
		}
		if proposal.Status != string(ProposalPending) {
			return core.ErrConflict
		}
		now := s.now().UTC()
		status := ProposalRejected
		if input.Decision == DecisionAccept {
			status = ProposalAccepted
		}
		result := transaction.Model(&storage.WorkflowIntentProposalModel{}).
			Where("id = ? AND version = ? AND status = ?", proposal.ID, proposal.Version, ProposalPending).
			Updates(map[string]any{
				"status": status, "version": proposal.Version + 1, "decision_reason": input.Reason,
				"decided_by": actorID, "decided_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return core.ErrConflict
		}
		proposal.Status = string(status)
		proposal.Version++
		proposal.DecisionReason = input.Reason
		proposal.DecidedBy = &actorID
		proposal.DecidedAt = &now
		if status == ProposalAccepted {
			payload, err := commandPayloadFromProposal(proposal)
			if err != nil {
				return err
			}
			encoded, err := platformdomain.CanonicalJSON(payload)
			if err != nil {
				return err
			}
			model := storage.ConversationCommandModel{
				ID: uuid.New(), ProjectID: projectID, ConversationID: conversationID, ProposalID: proposal.ID,
				Kind: proposal.Kind, Status: string(CommandPending), Version: 1, Payload: encoded,
				AcceptedBy: actorID, CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&model).Error; err != nil {
				if uniqueViolation(err) {
					return core.ErrConflict
				}
				return err
			}
			command = &model
		}
		action := "conversation.intent.rejected"
		if status == ProposalAccepted {
			action = "conversation.intent.accepted"
		}
		metadata := map[string]any{"conversationId": conversationID.String(), "decision": input.Decision}
		if command != nil {
			metadata["commandId"] = command.ID.String()
		}
		if err := conversationAudit(transaction, projectID, actorID, action, "workflow_intent_proposal", proposal.ID.String(), metadata); err != nil {
			return err
		}
		return conversationOutbox(transaction, "workflow_intent_proposal", proposal.ID.String(), action, map[string]any{
			"projectId": projectID.String(), "conversationId": conversationID.String(), "proposalId": proposal.ID.String(), "commandId": metadata["commandId"],
		})
	})
	if err != nil {
		return WorkflowIntentProposal{}, nil, err
	}
	proposalResult, err := proposalFromModel(proposal)
	if err != nil {
		return WorkflowIntentProposal{}, nil, err
	}
	if command == nil {
		return proposalResult, nil, nil
	}
	commandResult, err := commandFromModel(*command)
	if err != nil {
		return WorkflowIntentProposal{}, nil, err
	}
	return proposalResult, &commandResult, nil
}

func (s *GORMStore) GetCommand(ctx context.Context, projectID, conversationID, commandID uuid.UUID) (ConversationCommand, error) {
	var model storage.ConversationCommandModel
	err := s.database.WithContext(ctx).Where("id = ? AND project_id = ? AND conversation_id = ?", commandID, projectID, conversationID).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ConversationCommand{}, core.ErrNotFound
	}
	if err != nil {
		return ConversationCommand{}, fmt.Errorf("load conversation command: %w", err)
	}
	return commandFromModel(model)
}

func (s *GORMStore) ListCommands(ctx context.Context, projectID, conversationID uuid.UUID, options ListOptions) (CommandPage, error) {
	if err := s.ensureConversation(ctx, projectID, conversationID); err != nil {
		return CommandPage{}, err
	}
	query := s.database.WithContext(ctx).Where("project_id = ? AND conversation_id = ?", projectID, conversationID)
	if options.Cursor != "" {
		createdAt, id, err := decodeHistoryCursor(options.Cursor)
		if err != nil {
			return CommandPage{}, err
		}
		query = query.Where("(created_at, id) < (?, ?)", createdAt, id)
	}
	var models []storage.ConversationCommandModel
	if err := query.Order("created_at DESC, id DESC").Limit(options.Limit + 1).Find(&models).Error; err != nil {
		return CommandPage{}, fmt.Errorf("list conversation commands: %w", err)
	}
	page := CommandPage{Items: make([]ConversationCommand, 0, min(len(models), options.Limit))}
	for index, model := range models {
		if index == options.Limit {
			last := models[index-1]
			page.NextCursor = encodeHistoryCursor(last.CreatedAt, last.ID)
			break
		}
		command, err := commandFromModel(model)
		if err != nil {
			return CommandPage{}, err
		}
		page.Items = append(page.Items, command)
	}
	return page, nil
}

func (s *GORMStore) ClaimCommand(ctx context.Context, projectID, conversationID, commandID, actorID uuid.UUID, expectedETag string, lease time.Duration) (commandClaim, error) {
	var model storage.ConversationCommandModel
	token := uuid.New()
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ? AND conversation_id = ?", commandID, projectID, conversationID).Take(&model).Error; err != nil {
			return mapNotFound(err)
		}
		var proposal storage.WorkflowIntentProposalModel
		if err := transaction.Clauses(clause.Locking{Strength: "SHARE"}).
			Where("id = ? AND project_id = ? AND conversation_id = ?", model.ProposalID, projectID, conversationID).
			Take(&proposal).Error; err != nil {
			return mapNotFound(err)
		}
		if proposal.Status != string(ProposalAccepted) || proposal.Kind != model.Kind {
			return core.ErrConflict
		}
		if CommandETag(model.ID.String(), model.Version) != expectedETag || model.Status != string(CommandPending) {
			return core.ErrConflict
		}
		now := s.now().UTC()
		if model.ExecutionClaim != nil && model.ClaimExpiresAt != nil && model.ClaimExpiresAt.After(now) {
			return core.ErrConflict
		}
		if model.ExecutionActorID != nil && *model.ExecutionActorID != actorID {
			return core.ErrForbidden
		}
		expiresAt := now.Add(lease)
		result := transaction.Model(&storage.ConversationCommandModel{}).
			Where("id = ? AND version = ? AND status = ?", model.ID, model.Version, CommandPending).
			Updates(map[string]any{
				"execution_actor_id": actorID, "execution_claim": token, "claim_expires_at": expiresAt,
				"version": model.Version + 1, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return core.ErrConflict
		}
		model.ExecutionActorID = &actorID
		model.ExecutionClaim = &token
		model.ClaimExpiresAt = &expiresAt
		model.Version++
		model.UpdatedAt = now
		return nil
	})
	if err != nil {
		return commandClaim{}, err
	}
	command, err := commandFromModel(model)
	if err != nil {
		return commandClaim{}, err
	}
	return commandClaim{Command: command, Token: token}, nil
}

func (s *GORMStore) CompleteCommand(ctx context.Context, claim commandClaim, actorID uuid.UUID, status CommandStatus, result json.RawMessage, failure *CommandFailure) (ConversationCommand, error) {
	commandID, _ := uuid.Parse(claim.Command.ID)
	projectID, _ := uuid.Parse(claim.Command.ProjectID)
	var model storage.ConversationCommandModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", commandID).Take(&model).Error; err != nil {
			return mapNotFound(err)
		}
		if model.Status != string(CommandPending) || model.ExecutionClaim == nil || *model.ExecutionClaim != claim.Token || model.ExecutionActorID == nil || *model.ExecutionActorID != actorID {
			return core.ErrConflict
		}
		now := s.now().UTC()
		updates := map[string]any{
			"status": status, "version": model.Version + 1, "result": nullableJSON(result),
			"failure": nil, "execution_claim": nil, "claim_expires_at": nil, "updated_at": now,
		}
		if failure != nil {
			encoded, err := platformdomain.CanonicalJSON(failure)
			if err != nil {
				return err
			}
			updates["failure"] = encoded
		}
		switch status {
		case CommandExecuted:
			updates["executed_by"] = actorID
			updates["executed_at"] = now
		case CommandFailed:
			updates["executed_by"] = actorID
			updates["failed_at"] = now
		default:
			return core.ErrInvalidInput
		}
		resultUpdate := transaction.Model(&storage.ConversationCommandModel{}).
			Where("id = ? AND version = ? AND status = ? AND execution_claim = ?", model.ID, model.Version, CommandPending, claim.Token).
			Updates(updates)
		if resultUpdate.Error != nil {
			return resultUpdate.Error
		}
		if resultUpdate.RowsAffected != 1 {
			return core.ErrConflict
		}
		if err := transaction.Where("id = ?", model.ID).Take(&model).Error; err != nil {
			return err
		}
		action := "conversation.command.executed"
		if status == CommandFailed {
			action = "conversation.command.failed"
		}
		if err := conversationAudit(transaction, projectID, actorID, action, "conversation_command", model.ID.String(), map[string]any{
			"conversationId": model.ConversationID.String(), "proposalId": model.ProposalID.String(), "kind": model.Kind,
		}); err != nil {
			return err
		}
		return conversationOutbox(transaction, "conversation_command", model.ID.String(), action, map[string]any{
			"projectId": projectID.String(), "conversationId": model.ConversationID.String(), "commandId": model.ID.String(), "status": status,
		})
	})
	if err != nil {
		return ConversationCommand{}, err
	}
	return commandFromModel(model)
}

func (s *GORMStore) CompleteWorkbenchCommand(
	ctx context.Context,
	projectID, conversationID, commandID, actorID uuid.UUID,
	expectedETag string,
	lease time.Duration,
	result WorkbenchExecutionResult,
) (ConversationCommand, error) {
	if lease <= 0 {
		return ConversationCommand{}, fmt.Errorf("%w: command claim lease", core.ErrInvalidInput)
	}
	encodedResult, err := platformdomain.CanonicalJSON(result)
	if err != nil {
		return ConversationCommand{}, fmt.Errorf("canonicalize workbench result: %w", err)
	}
	var model storage.ConversationCommandModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ? AND conversation_id = ?", commandID, projectID, conversationID).
			Take(&model).Error; err != nil {
			return mapNotFound(err)
		}
		if model.Kind != string(IntentWorkbenchInstruction) || model.Status != string(CommandPending) ||
			CommandETag(model.ID.String(), model.Version) != expectedETag {
			return core.ErrConflict
		}
		now := s.now().UTC()
		if model.ExecutionClaim != nil && model.ClaimExpiresAt != nil && model.ClaimExpiresAt.After(now) {
			return core.ErrConflict
		}
		if model.ExecutionActorID != nil && *model.ExecutionActorID != actorID {
			return core.ErrForbidden
		}
		var reviewedProposal storage.WorkflowIntentProposalModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND project_id = ? AND conversation_id = ?", model.ProposalID, projectID, conversationID,
		).Take(&reviewedProposal).Error; err != nil {
			return mapNotFound(err)
		}
		if reviewedProposal.Status != string(ProposalAccepted) ||
			reviewedProposal.Kind != string(IntentWorkbenchInstruction) || reviewedProposal.Kind != model.Kind {
			return core.ErrConflict
		}
		command, err := commandFromModel(model)
		if err != nil {
			return err
		}
		claimToken := uuid.New()
		claimExpiresAt := now.Add(lease)
		claim := transaction.Model(&storage.ConversationCommandModel{}).Where(
			"id = ? AND version = ? AND status = ?", model.ID, model.Version, CommandPending,
		).Updates(map[string]any{
			"execution_actor_id": actorID, "execution_claim": claimToken, "claim_expires_at": claimExpiresAt,
			"version": model.Version + 1, "updated_at": now,
		})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return core.ErrConflict
		}
		model.ExecutionActorID = &actorID
		model.ExecutionClaim = &claimToken
		model.ClaimExpiresAt = &claimExpiresAt
		model.Version++
		if err := validateWorkbenchResultOnDatabase(transaction, projectID, command.Payload, result, true); err != nil {
			return err
		}
		update := transaction.Model(&storage.ConversationCommandModel{}).Where(
			"id = ? AND version = ? AND status = ? AND execution_claim = ?",
			model.ID, model.Version, CommandPending, claimToken,
		).Updates(map[string]any{
			"status": CommandExecuted, "version": model.Version + 1, "result": encodedResult, "failure": nil,
			"execution_actor_id": actorID, "execution_claim": nil, "claim_expires_at": nil,
			"executed_by": actorID, "executed_at": now, "updated_at": now,
		})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return core.ErrConflict
		}
		if err := transaction.Where("id = ?", model.ID).Take(&model).Error; err != nil {
			return err
		}
		if err := conversationAudit(transaction, projectID, actorID, "conversation.command.executed", "conversation_command", model.ID.String(), map[string]any{
			"conversationId": model.ConversationID.String(), "proposalId": model.ProposalID.String(), "kind": model.Kind,
		}); err != nil {
			return err
		}
		return conversationOutbox(transaction, "conversation_command", model.ID.String(), "conversation.command.executed", map[string]any{
			"projectId": projectID.String(), "conversationId": model.ConversationID.String(),
			"commandId": model.ID.String(), "status": CommandExecuted,
		})
	})
	if err != nil {
		return ConversationCommand{}, err
	}
	return commandFromModel(model)
}

func (s *GORMStore) RejectCommand(ctx context.Context, projectID, conversationID, commandID, actorID uuid.UUID, expectedETag, reason string) (ConversationCommand, error) {
	var model storage.ConversationCommandModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ? AND conversation_id = ?", commandID, projectID, conversationID).Take(&model).Error; err != nil {
			return mapNotFound(err)
		}
		if CommandETag(model.ID.String(), model.Version) != expectedETag || model.Status != string(CommandPending) {
			return core.ErrConflict
		}
		now := s.now().UTC()
		if model.ExecutionClaim != nil && model.ClaimExpiresAt != nil && model.ClaimExpiresAt.After(now) {
			return core.ErrConflict
		}
		resultPayload, _ := platformdomain.CanonicalJSON(map[string]any{"reason": reason})
		result := transaction.Model(&storage.ConversationCommandModel{}).
			Where("id = ? AND version = ? AND status = ?", model.ID, model.Version, CommandPending).
			Updates(map[string]any{
				"status": CommandRejected, "version": model.Version + 1, "result": resultPayload,
				"execution_claim": nil, "claim_expires_at": nil, "rejected_by": actorID,
				"rejected_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return core.ErrConflict
		}
		if err := transaction.Where("id = ?", model.ID).Take(&model).Error; err != nil {
			return err
		}
		if err := conversationAudit(transaction, projectID, actorID, "conversation.command.rejected", "conversation_command", model.ID.String(), map[string]any{
			"conversationId": conversationID.String(), "proposalId": model.ProposalID.String(),
		}); err != nil {
			return err
		}
		return conversationOutbox(transaction, "conversation_command", model.ID.String(), "conversation.command.rejected", map[string]any{
			"projectId": projectID.String(), "conversationId": conversationID.String(), "commandId": model.ID.String(),
		})
	})
	if err != nil {
		return ConversationCommand{}, err
	}
	return commandFromModel(model)
}

func (s *GORMStore) ValidateWorkbenchResult(ctx context.Context, projectID uuid.UUID, payload CommandPayload, result WorkbenchExecutionResult) error {
	return validateWorkbenchResultOnDatabase(s.database.WithContext(ctx), projectID, payload, result, false)
}

func validateWorkbenchResultOnDatabase(
	database *gorm.DB,
	projectID uuid.UUID,
	payload CommandPayload,
	result WorkbenchExecutionResult,
	lock bool,
) error {
	runID, err := uuid.Parse(result.RunID)
	if err != nil {
		return fmt.Errorf("%w: workbench run id", core.ErrInvalidInput)
	}
	bundleID, err := uuid.Parse(result.BundleID)
	if err != nil {
		return fmt.Errorf("%w: workbench bundle id", core.ErrInvalidInput)
	}
	implementationProposalID, err := uuid.Parse(result.ImplementationProposalID)
	if err != nil {
		return fmt.Errorf("%w: implementation proposal id", core.ErrInvalidInput)
	}
	definitionVersionID, err := uuid.Parse(payload.DefinitionVersionID)
	if err != nil {
		return fmt.Errorf("%w: workflow definition version id", core.ErrInvalidInput)
	}
	expectedRunID, err := uuid.Parse(payload.Workbench.ExpectedRunID)
	if err != nil || expectedRunID != runID {
		return core.ErrConflict
	}
	expectedBundleID, err := uuid.Parse(payload.Workbench.ExpectedBundleID)
	if err != nil {
		return fmt.Errorf("%w: expected workbench bundle id", core.ErrInvalidInput)
	}
	if lock {
		var project storage.ProjectModel
		if err := database.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
			Where("id = ?", projectID).Take(&project).Error; err != nil {
			return mapNotFound(err)
		}
	}
	if err := validateManifestIntentWithLock(database, projectID, payload.ManifestIntent, lock); err != nil {
		return err
	}
	var run storage.WorkflowRunModel
	runQuery := database.Where(
		"id = ? AND project_id = ? AND definition_version_id = ? AND status IN ?",
		runID, projectID, definitionVersionID, activeWorkbenchRunStatuses,
	)
	if lock {
		runQuery = runQuery.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := runQuery.Take(&run).Error; err != nil {
		return mapNotFound(err)
	}
	currentWorkspaceRevisionID, err := currentApprovedWorkspaceRevisionID(database, projectID, lock)
	if err != nil {
		return err
	}
	var activeLeaf storage.ApplicationBuildManifestModel
	if lock {
		_, activeLeaf, err = loadLockedAuthoritativeWorkbenchLeaf(
			database, projectID, runID, expectedBundleID, bundleID,
		)
		if err != nil {
			return err
		}
	} else {
		expectedRoot, _, targetErr := loadAuthoritativeWorkbenchLeaf(database, projectID, runID, expectedBundleID)
		if targetErr != nil {
			return targetErr
		}
		resultRoot, resultLeaf, resultErr := loadAuthoritativeWorkbenchLeaf(database, projectID, runID, bundleID)
		if resultErr != nil {
			return resultErr
		}
		if resultRoot.ID != expectedRoot.ID || resultLeaf.ID != bundleID {
			return core.ErrConflict
		}
		activeLeaf = resultLeaf
	}
	if !sameOptionalUUID(activeLeaf.WorkspaceRevisionID, currentWorkspaceRevisionID) {
		return core.ErrConflict
	}
	var implementationProposal storage.ImplementationProposalModel
	proposalQuery := database.Where("id = ? AND project_id = ?", implementationProposalID, projectID)
	if lock {
		proposalQuery = proposalQuery.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := proposalQuery.Take(&implementationProposal).Error; err != nil {
		return mapNotFound(err)
	}
	if implementationProposal.BuildManifestID != activeLeaf.ID ||
		!containsString(executableImplementationProposalStatuses, implementationProposal.Status) ||
		implementationProposal.AppliedAt != nil || implementationProposal.AppliedBy != nil {
		return core.ErrConflict
	}
	return nil
}

func currentApprovedWorkspaceRevisionID(database *gorm.DB, projectID uuid.UUID, lock bool) (*uuid.UUID, error) {
	var workspaceRows []storage.ArtifactModel
	workspaceQuery := database.Where(
		"project_id = ? AND kind = ?", projectID, "workspace",
	).Order("artifact_key ASC, id ASC")
	if lock {
		workspaceQuery = workspaceQuery.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := workspaceQuery.Find(&workspaceRows).Error; err != nil {
		return nil, err
	}
	workspaces := make([]storage.ArtifactModel, 0, 1)
	for _, workspace := range workspaceRows {
		if workspace.Lifecycle == "active" {
			workspaces = append(workspaces, workspace)
		}
	}
	if len(workspaces) == 0 {
		return nil, nil
	}
	if len(workspaces) != 1 || workspaces[0].LatestApprovedRevisionID == nil {
		return nil, core.ErrConflict
	}
	revisionID := *workspaces[0].LatestApprovedRevisionID
	var revision storage.ArtifactRevisionModel
	revisionQuery := database.Where(
		"id = ? AND artifact_id = ? AND workflow_status = ?",
		revisionID, workspaces[0].ID, "approved",
	)
	if lock {
		revisionQuery = revisionQuery.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := revisionQuery.Take(&revision).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &revisionID, nil
}

func sameOptionalUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (s *GORMStore) ensureConversation(ctx context.Context, projectID, conversationID uuid.UUID) error {
	var count int64
	if err := s.database.WithContext(ctx).Model(&storage.ConversationModel{}).
		Where("id = ? AND project_id = ?", conversationID, projectID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return core.ErrNotFound
	}
	return nil
}

func (s *GORMStore) ensureActiveConversation(ctx context.Context, projectID, conversationID uuid.UUID) error {
	var model storage.ConversationModel
	if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", conversationID, projectID).Take(&model).Error; err != nil {
		return mapNotFound(err)
	}
	if model.Status != string(ConversationActive) {
		return core.ErrConflict
	}
	return nil
}

func lockActiveConversation(transaction *gorm.DB, projectID, conversationID uuid.UUID) (storage.ConversationModel, error) {
	var model storage.ConversationModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND project_id = ?", conversationID, projectID).Take(&model).Error; err != nil {
		return model, mapNotFound(err)
	}
	if model.Status != string(ConversationActive) {
		return model, core.ErrConflict
	}
	return model, nil
}

func nextMessageSequence(transaction *gorm.DB, conversationID uuid.UUID) (uint64, error) {
	var latest uint64
	if err := transaction.Model(&storage.ConversationMessageModel{}).Where("conversation_id = ?", conversationID).
		Select("COALESCE(MAX(sequence), 0)").Scan(&latest).Error; err != nil {
		return 0, err
	}
	return latest + 1, nil
}

func validateDefinitionVersion(transaction *gorm.DB, projectID, definitionVersionID uuid.UUID, requirePublished bool, manifestJobType string) error {
	var row struct {
		Content json.RawMessage
	}
	query := transaction.Table("workflow_definition_versions AS version").
		Select("version.content").
		Joins("JOIN workflow_definitions AS definition ON definition.id = version.definition_id").
		Where("version.id = ? AND (definition.project_id IS NULL OR definition.project_id = ?)", definitionVersionID, projectID)
	if requirePublished {
		query = query.Where("version.published = TRUE")
	}
	if err := query.Take(&row).Error; err != nil {
		return mapNotFound(err)
	}
	if requirePublished {
		return validateStartDefinitionCompatibility(manifestJobType, row.Content)
	}
	return nil
}

func validateStartDefinitionCompatibility(manifestJobType string, content json.RawMessage) error {
	manifestJobType = strings.TrimSpace(manifestJobType)
	if manifestJobType == "" {
		return fmt.Errorf("%w: input manifest job type", core.ErrInvalidInput)
	}
	var definition platformdomain.WorkflowDefinition
	if err := json.Unmarshal(content, &definition); err != nil {
		return fmt.Errorf("%w: workflow definition content", core.ErrInvalidInput)
	}
	if err := runtime.ValidateStartManifestJobType(definition, manifestJobType); err != nil {
		return fmt.Errorf("%w: blueprint selection workflow and %s input manifest must be used together", core.ErrInvalidInput, core.BlueprintSelectionJobType)
	}
	return nil
}

func validateManifestIntent(transaction *gorm.DB, projectID uuid.UUID, intent ManifestIntent) error {
	return validateManifestIntentWithLock(transaction, projectID, intent, false)
}

func validateManifestIntentWithLock(transaction *gorm.DB, projectID uuid.UUID, intent ManifestIntent, lock bool) error {
	manifestID, err := uuid.Parse(intent.InputManifest.ID)
	if err != nil {
		return core.ErrInvalidInput
	}
	var manifest storage.InputManifestModel
	query := transaction.Where("id = ? AND project_id = ?", manifestID, projectID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&manifest).Error; err != nil {
		return mapNotFound(err)
	}
	if manifest.ManifestHash != intent.InputManifest.Hash {
		return core.ErrConflict
	}
	return nil
}

func validateExactSourceRefs(transaction *gorm.DB, projectID uuid.UUID, refs []platformdomain.ArtifactRef) error {
	for _, ref := range refs {
		artifactID, _ := uuid.Parse(ref.ArtifactID)
		revisionID, _ := uuid.Parse(ref.RevisionID)
		var count int64
		err := transaction.Table("artifact_revisions AS revision").
			Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
			Where("revision.id = ? AND revision.artifact_id = ? AND revision.content_hash = ? AND artifact.project_id = ?", revisionID, artifactID, ref.ContentHash, projectID).
			Count(&count).Error
		if err != nil {
			return err
		}
		if count != 1 {
			return core.ErrNotFound
		}
	}
	return nil
}

func validateExpectedWorkbenchTarget(transaction *gorm.DB, projectID uuid.UUID, input CreateIntentProposalInput) error {
	runID, err := uuid.Parse(input.WorkbenchInstruction.ExpectedRunID)
	if err != nil {
		return core.ErrInvalidInput
	}
	definitionVersionID, err := uuid.Parse(input.SuggestedDefinitionVersionID)
	if err != nil {
		return core.ErrInvalidInput
	}
	bundleID, err := uuid.Parse(input.WorkbenchInstruction.ExpectedBundleID)
	if err != nil {
		return core.ErrInvalidInput
	}
	if err := validateManifestIntent(transaction, projectID, input.ManifestIntent); err != nil {
		return err
	}
	var run storage.WorkflowRunModel
	if err := transaction.Where(
		"id = ? AND project_id = ? AND definition_version_id = ? AND status IN ?",
		runID, projectID, definitionVersionID, activeWorkbenchRunStatuses,
	).Take(&run).Error; err != nil {
		return mapNotFound(err)
	}
	_, _, err = loadAuthoritativeWorkbenchLeaf(transaction, projectID, runID, bundleID)
	return err
}

func loadAuthoritativeWorkbenchLeaf(
	database *gorm.DB,
	projectID uuid.UUID,
	runID uuid.UUID,
	requestedBundleID uuid.UUID,
) (storage.ApplicationBuildManifestModel, storage.ApplicationBuildManifestModel, error) {
	var requested storage.ApplicationBuildManifestModel
	if err := database.Where("id = ? AND project_id = ? AND workflow_run_id = ?", requestedBundleID, projectID, runID).
		Take(&requested).Error; err != nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, mapNotFound(err)
	}
	rootID := requested.RootManifestID
	if rootID == uuid.Nil {
		rootID = requested.ID
	}
	var root storage.ApplicationBuildManifestModel
	if err := database.Where("id = ? AND project_id = ? AND workflow_run_id = ?", rootID, projectID, runID).
		Take(&root).Error; err != nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, mapNotFound(err)
	}
	if root.ID != rootID || root.RootManifestID != root.ID || root.DerivedFromID != nil ||
		root.WorkflowRunID == nil || *root.WorkflowRunID != runID ||
		root.ManifestGroupKey == nil || root.RootOrdinal == nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	var leaves []storage.ApplicationBuildManifestModel
	if err := database.Where(
		"project_id = ? AND workflow_run_id = ? AND root_manifest_id = ? AND status = ? AND invalidated_at IS NULL",
		projectID, runID, rootID, "frozen",
	).Where("NOT EXISTS (SELECT 1 FROM application_build_manifests AS child WHERE child.derived_from_id = application_build_manifests.id)").
		Order("created_at DESC, id DESC").Limit(2).Find(&leaves).Error; err != nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, err
	}
	if len(leaves) != 1 {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	leaf := leaves[0]
	if leaf.WorkflowRunID == nil || *leaf.WorkflowRunID != runID || leaf.RootManifestID != root.ID ||
		!sameWorkbenchManifestCoordinate(root, leaf) {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	if requested.ID != root.ID && requested.ID != leaf.ID {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	if requested.RootManifestID != root.ID || !sameWorkbenchManifestCoordinate(root, requested) {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	return root, leaf, nil
}

func loadLockedAuthoritativeWorkbenchLeaf(
	database *gorm.DB,
	projectID uuid.UUID,
	runID uuid.UUID,
	expectedBundleID uuid.UUID,
	resultBundleID uuid.UUID,
) (storage.ApplicationBuildManifestModel, storage.ApplicationBuildManifestModel, error) {
	var expectedBundle storage.ApplicationBuildManifestModel
	if err := database.Where(
		"id = ? AND project_id = ? AND workflow_run_id = ?", expectedBundleID, projectID, runID,
	).Take(&expectedBundle).Error; err != nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, mapNotFound(err)
	}
	var resultBundle storage.ApplicationBuildManifestModel
	if err := database.Where(
		"id = ? AND project_id = ? AND workflow_run_id = ?", resultBundleID, projectID, runID,
	).Take(&resultBundle).Error; err != nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, mapNotFound(err)
	}
	expectedRootID := expectedBundle.RootManifestID
	if expectedRootID == uuid.Nil {
		expectedRootID = expectedBundle.ID
	}
	resultRootID := resultBundle.RootManifestID
	if resultRootID == uuid.Nil {
		resultRootID = resultBundle.ID
	}
	if expectedRootID != resultRootID {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	var root storage.ApplicationBuildManifestModel
	if err := database.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"id = ? AND project_id = ? AND workflow_run_id = ?", expectedRootID, projectID, runID,
	).Take(&root).Error; err != nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, mapNotFound(err)
	}
	if root.RootManifestID != root.ID || root.DerivedFromID != nil || root.WorkflowRunID == nil ||
		*root.WorkflowRunID != runID || root.ManifestGroupKey == nil || root.RootOrdinal == nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	var leaves []storage.ApplicationBuildManifestModel
	if err := database.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"project_id = ? AND workflow_run_id = ? AND root_manifest_id = ? AND status = ? AND invalidated_at IS NULL",
		projectID, runID, root.ID, "frozen",
	).Where("NOT EXISTS (SELECT 1 FROM application_build_manifests AS child WHERE child.derived_from_id = application_build_manifests.id)").
		Order("created_at DESC, id DESC").Limit(2).Find(&leaves).Error; err != nil {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, err
	}
	if len(leaves) != 1 {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	leaf := leaves[0]
	if leaf.WorkflowRunID == nil || *leaf.WorkflowRunID != runID || leaf.RootManifestID != root.ID ||
		!sameWorkbenchManifestCoordinate(root, leaf) {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	if expectedBundleID != root.ID && expectedBundleID != leaf.ID {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	if resultBundleID != leaf.ID {
		return storage.ApplicationBuildManifestModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	return root, leaf, nil
}

func sameWorkbenchManifestCoordinate(left, right storage.ApplicationBuildManifestModel) bool {
	if left.RootOrdinal == nil || right.RootOrdinal == nil || *left.RootOrdinal != *right.RootOrdinal ||
		left.ManifestGroupKey == nil || right.ManifestGroupKey == nil {
		return false
	}
	return strings.TrimSpace(*left.ManifestGroupKey) != "" && *left.ManifestGroupKey == *right.ManifestGroupKey
}

func commandPayloadFromProposal(model storage.WorkflowIntentProposalModel) (CommandPayload, error) {
	var sources []platformdomain.ArtifactRef
	var manifest ManifestIntent
	var instruction WorkbenchInstruction
	if err := json.Unmarshal(model.SourceRefs, &sources); err != nil {
		return CommandPayload{}, fmt.Errorf("decode proposal source refs: %w", err)
	}
	if err := json.Unmarshal(model.ManifestIntent, &manifest); err != nil {
		return CommandPayload{}, fmt.Errorf("decode proposal manifest intent: %w", err)
	}
	if err := json.Unmarshal(model.WorkbenchInstruction, &instruction); err != nil {
		return CommandPayload{}, fmt.Errorf("decode proposal workbench instruction: %w", err)
	}
	return CommandPayload{
		DefinitionVersionID: model.SuggestedDefinitionVersionID.String(), Scope: append(json.RawMessage(nil), model.Scope...),
		SourceRefs: sources, ManifestIntent: manifest, Workbench: instruction,
	}, nil
}

func conversationFromModel(model storage.ConversationModel) Conversation {
	return Conversation{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), Title: model.Title,
		Status: ConversationStatus(model.Status), Version: model.Version, ETag: ConversationETag(model.ID.String(), model.Version),
		CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt, ArchivedAt: model.ArchivedAt,
	}
}

func messageFromModel(model storage.ConversationMessageModel) Message {
	return Message{
		ID: model.ID.String(), ConversationID: model.ConversationID.String(), Sequence: model.Sequence,
		Role: MessageRole(model.Role), Content: model.Content, ProposalID: uuidString(model.ProposalID),
		CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt,
	}
}

func proposalFromModel(model storage.WorkflowIntentProposalModel) (WorkflowIntentProposal, error) {
	payload, err := commandPayloadFromProposal(model)
	if err != nil {
		return WorkflowIntentProposal{}, err
	}
	return WorkflowIntentProposal{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), ConversationID: model.ConversationID.String(),
		TriggerMessageID: model.TriggerMessageID.String(), AssistantMessageID: model.AssistantMessageID.String(),
		Kind: IntentKind(model.Kind), Status: ProposalStatus(model.Status), Version: model.Version,
		ETag: ProposalETag(model.ID.String(), model.Version), SuggestedDefinitionVersionID: model.SuggestedDefinitionVersionID.String(),
		Scope: payload.Scope, SourceRefs: payload.SourceRefs, ManifestIntent: payload.ManifestIntent,
		WorkbenchInstruction: payload.Workbench, Origin: ProposalOrigin(model.Origin), AI: aiProvenanceFromModel(model),
		DecisionReason: model.DecisionReason, ProposedBy: model.ProposedBy.String(),
		DecidedBy: uuidString(model.DecidedBy), CreatedAt: model.CreatedAt, DecidedAt: model.DecidedAt,
	}, nil
}

func commandFromModel(model storage.ConversationCommandModel) (ConversationCommand, error) {
	var payload CommandPayload
	if err := json.Unmarshal(model.Payload, &payload); err != nil {
		return ConversationCommand{}, fmt.Errorf("decode conversation command payload: %w", err)
	}
	var failure *CommandFailure
	if len(model.Failure) > 0 && string(model.Failure) != "null" {
		var decoded CommandFailure
		if err := json.Unmarshal(model.Failure, &decoded); err != nil {
			return ConversationCommand{}, fmt.Errorf("decode conversation command failure: %w", err)
		}
		failure = &decoded
	}
	return ConversationCommand{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), ConversationID: model.ConversationID.String(),
		ProposalID: model.ProposalID.String(), Kind: IntentKind(model.Kind), Status: CommandStatus(model.Status),
		Version: model.Version, ETag: CommandETag(model.ID.String(), model.Version), Payload: payload,
		Result: append(json.RawMessage(nil), model.Result...), Failure: failure, AcceptedBy: model.AcceptedBy.String(),
		ExecutionActorID: uuidString(model.ExecutionActorID), ExecutedBy: uuidString(model.ExecutedBy), RejectedBy: uuidString(model.RejectedBy),
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt, ExecutedAt: model.ExecutedAt,
		RejectedAt: model.RejectedAt, FailedAt: model.FailedAt,
	}, nil
}

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return core.ErrNotFound
	}
	return err
}

func uniqueViolation(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint")
}

func uuidString(value *uuid.UUID) *string {
	if value == nil {
		return nil
	}
	result := value.String()
	return &result
}

func stringPointer(value string) *string {
	result := value
	return &result
}

func aiProvenanceFromModel(model storage.WorkflowIntentProposalModel) *AIProvenance {
	if model.AIProvider == nil || model.AIModel == nil {
		return nil
	}
	result := &AIProvenance{Provider: *model.AIProvider, Model: *model.AIModel}
	if model.AIResponseID != nil {
		result.ResponseID = *model.AIResponseID
	}
	return result
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func conversationAudit(transaction *gorm.DB, projectID, actorID uuid.UUID, action, targetType, targetID string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	payload, err := platformdomain.CanonicalJSON(metadata)
	if err != nil {
		return err
	}
	var requestID *string
	if value := core.RequestIDFromContext(transaction.Statement.Context); value != "" {
		requestID = &value
	}
	return transaction.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: &projectID, ActorID: &actorID, RequestID: requestID,
		Action: action, TargetType: targetType, TargetID: targetID, Metadata: payload, CreatedAt: time.Now().UTC(),
	}).Error
}

func conversationOutbox(transaction *gorm.DB, aggregateType, aggregateID, eventType string, payload any) error {
	encoded, err := platformdomain.CanonicalJSON(payload)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return transaction.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: aggregateType, AggregateID: aggregateID, EventType: eventType,
		Subject: "worksflow.conversation.event", Payload: encoded, Headers: json.RawMessage(`{}`),
		AvailableAt: now, CreatedAt: now,
	}).Error
}
