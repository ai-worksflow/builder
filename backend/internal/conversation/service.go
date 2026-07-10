package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
	"gorm.io/gorm"
)

type Authorizer interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type WorkflowRuntime interface {
	Start(context.Context, string, string, runtime.StartRequest) (*runtime.RunRecord, error)
	GetRun(context.Context, string, string, string) (*runtime.RunRecord, error)
}

type ManifestResolver interface {
	GetManifest(context.Context, string) (platformdomain.InputManifest, error)
}

type conversationStore interface {
	CreateConversation(context.Context, uuid.UUID, uuid.UUID, string) (Conversation, error)
	GetConversation(context.Context, uuid.UUID, uuid.UUID) (Conversation, error)
	ListConversations(context.Context, uuid.UUID, ListOptions) (ConversationPage, error)
	UpdateConversation(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, UpdateConversationInput) (Conversation, error)
	AppendUserMessage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) (Message, error)
	ListMessages(context.Context, uuid.UUID, uuid.UUID, ListOptions) (MessagePage, error)
	CreateIntentProposal(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, CreateIntentProposalInput, ProposalProvenance, string) (WorkflowIntentProposal, Message, error)
	IntentGenerationContext(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []uuid.UUID, []platformdomain.ArtifactRef, ManifestIntent, string) (intentGenerationContext, error)
	GetProposal(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (WorkflowIntentProposal, error)
	ListProposals(context.Context, uuid.UUID, uuid.UUID, ListOptions) (ProposalPage, error)
	DecideProposal(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, DecideProposalInput) (WorkflowIntentProposal, *ConversationCommand, error)
	GetCommand(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (ConversationCommand, error)
	ListCommands(context.Context, uuid.UUID, uuid.UUID, ListOptions) (CommandPage, error)
	ClaimCommand(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, time.Duration) (commandClaim, error)
	CompleteCommand(context.Context, commandClaim, uuid.UUID, CommandStatus, json.RawMessage, *CommandFailure) (ConversationCommand, error)
	CompleteWorkbenchCommand(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, time.Duration, WorkbenchExecutionResult) (ConversationCommand, error)
	RejectCommand(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, string) (ConversationCommand, error)
	ValidateWorkbenchResult(context.Context, uuid.UUID, CommandPayload, WorkbenchExecutionResult) error
}

type ServiceDependencies struct {
	Database   *gorm.DB
	Access     Authorizer
	Workflow   WorkflowRuntime
	Manifests  ManifestResolver
	AIProvider ai.Provider
}

type Service struct {
	store      conversationStore
	access     Authorizer
	workflow   WorkflowRuntime
	manifests  ManifestResolver
	provider   ai.Provider
	claimLease time.Duration
}

func NewService(dependencies ServiceDependencies) (*Service, error) {
	if dependencies.Database == nil || dependencies.Access == nil || dependencies.Workflow == nil || dependencies.Manifests == nil || dependencies.AIProvider == nil {
		return nil, errors.New("conversation database, access control, workflow runtime, manifest resolver and AI provider are required")
	}
	store, err := NewGORMStore(dependencies.Database)
	if err != nil {
		return nil, err
	}
	return newService(store, dependencies.Access, dependencies.Workflow, dependencies.Manifests, dependencies.AIProvider)
}

func newService(store conversationStore, access Authorizer, workflow WorkflowRuntime, manifests ManifestResolver, provider ai.Provider) (*Service, error) {
	if store == nil || access == nil || workflow == nil || manifests == nil || provider == nil {
		return nil, errors.New("conversation service dependencies are required")
	}
	return &Service{store: store, access: access, workflow: workflow, manifests: manifests, provider: provider, claimLease: 2 * time.Minute}, nil
}

func (s *Service) Create(ctx context.Context, projectID, actorID string, input CreateConversationInput) (Conversation, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return Conversation{}, err
	}
	title, err := normalizeTitle(input.Title)
	if err != nil {
		return Conversation{}, err
	}
	return s.store.CreateConversation(ctx, projectUUID, actorUUID, title)
}

func (s *Service) Get(ctx context.Context, projectID, conversationID, actorID string) (Conversation, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return Conversation{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return Conversation{}, err
	}
	return s.store.GetConversation(ctx, projectUUID, conversationUUID)
}

func (s *Service) List(ctx context.Context, projectID, actorID string, options ListOptions) (ConversationPage, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return ConversationPage{}, err
	}
	options, err = normalizeListOptions(options)
	if err != nil {
		return ConversationPage{}, err
	}
	return s.store.ListConversations(ctx, projectUUID, options)
}

func (s *Service) Update(ctx context.Context, projectID, conversationID, actorID, expectedETag string, input UpdateConversationInput) (Conversation, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return Conversation{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return Conversation{}, err
	}
	if strings.TrimSpace(expectedETag) == "" || input.Title == nil && input.Status == nil {
		return Conversation{}, fmt.Errorf("%w: conversation precondition or update", core.ErrInvalidInput)
	}
	if input.Title != nil {
		title, err := normalizeTitle(*input.Title)
		if err != nil {
			return Conversation{}, err
		}
		input.Title = &title
	}
	if input.Status != nil && *input.Status != ConversationActive && *input.Status != ConversationArchived {
		return Conversation{}, fmt.Errorf("%w: conversation status", core.ErrInvalidInput)
	}
	return s.store.UpdateConversation(ctx, projectUUID, conversationUUID, actorUUID, expectedETag, input)
}

func (s *Service) AppendMessage(ctx context.Context, projectID, conversationID, actorID string, input AppendMessageInput) (Message, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionComment)
	if err != nil {
		return Message{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return Message{}, err
	}
	content, err := normalizeContent(input.Content)
	if err != nil {
		return Message{}, err
	}
	return s.store.AppendUserMessage(ctx, projectUUID, conversationUUID, actorUUID, content)
}

func (s *Service) ListMessages(ctx context.Context, projectID, conversationID, actorID string, options ListOptions) (MessagePage, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return MessagePage{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return MessagePage{}, err
	}
	options, err = normalizeListOptions(options)
	if err != nil {
		return MessagePage{}, err
	}
	return s.store.ListMessages(ctx, projectUUID, conversationUUID, options)
}

func (s *Service) CreateIntentProposal(ctx context.Context, projectID, conversationID, actorID string, input CreateIntentProposalInput) (WorkflowIntentProposal, Message, error) {
	return s.createIntentProposal(ctx, projectID, conversationID, actorID, input, ProposalProvenance{Origin: ProposalOriginSubmitted})
}

func (s *Service) createIntentProposal(ctx context.Context, projectID, conversationID, actorID string, input CreateIntentProposalInput, provenance ProposalProvenance) (WorkflowIntentProposal, Message, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return WorkflowIntentProposal{}, Message{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return WorkflowIntentProposal{}, Message{}, err
	}
	input, err = normalizeProposalInput(input)
	if err != nil {
		return WorkflowIntentProposal{}, Message{}, err
	}
	manifest, err := s.validateManifestSources(ctx, projectID, input.ManifestIntent, input.SourceRefs)
	if err != nil {
		return WorkflowIntentProposal{}, Message{}, err
	}
	provenance, err = normalizeProposalProvenance(provenance)
	if err != nil {
		return WorkflowIntentProposal{}, Message{}, err
	}
	return s.store.CreateIntentProposal(ctx, projectUUID, conversationUUID, actorUUID, input, provenance, manifest.JobType)
}

func (s *Service) GenerateIntentProposal(ctx context.Context, projectID, conversationID, actorID string, input GenerateIntentProposalInput) (GeneratedIntentProposal, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	triggerID, err := parseID(input.TriggerMessageID, "trigger message id")
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	if len(input.CandidateDefinitionVersionIDs) == 0 || len(input.CandidateDefinitionVersionIDs) > 20 {
		return GeneratedIntentProposal{}, fmt.Errorf("%w: candidate definition versions", core.ErrInvalidInput)
	}
	candidates := make([]uuid.UUID, 0, len(input.CandidateDefinitionVersionIDs))
	candidateSet := make(map[string]struct{}, len(input.CandidateDefinitionVersionIDs))
	candidateIDs := make([]string, 0, len(input.CandidateDefinitionVersionIDs))
	for _, value := range input.CandidateDefinitionVersionIDs {
		parsed, err := parseID(value, "candidate definition version id")
		if err != nil {
			return GeneratedIntentProposal{}, err
		}
		if _, duplicate := candidateSet[parsed.String()]; duplicate {
			return GeneratedIntentProposal{}, fmt.Errorf("%w: duplicate candidate definition version", core.ErrInvalidInput)
		}
		candidateSet[parsed.String()] = struct{}{}
		candidates = append(candidates, parsed)
		candidateIDs = append(candidateIDs, parsed.String())
	}
	validationInput := CreateIntentProposalInput{
		TriggerMessageID: input.TriggerMessageID, AssistantContent: "Pending AI intent proposal",
		Kind: IntentStartWorkflow, SuggestedDefinitionVersionID: candidates[0].String(), Scope: json.RawMessage(`{}`),
		SourceRefs: input.SourceRefs, ManifestIntent: input.ManifestIntent,
		WorkbenchInstruction: WorkbenchInstruction{Objective: "Pending AI workbench instruction"},
	}
	validationInput, err = normalizeProposalInput(validationInput)
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	manifest, err := s.validateManifestSources(ctx, projectID, validationInput.ManifestIntent, validationInput.SourceRefs)
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	generationContext, err := s.store.IntentGenerationContext(
		ctx, projectUUID, conversationUUID, triggerID, candidates, validationInput.SourceRefs, validationInput.ManifestIntent, manifest.JobType,
	)
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	if err := validateLoadedStartCandidateCompatibility(manifest.JobType, candidateSet, generationContext.Definitions); err != nil {
		return GeneratedIntentProposal{}, err
	}
	providerInput, err := platformdomain.CanonicalJSON(map[string]any{
		"conversation": generationContext.Messages, "candidateWorkflowDefinitions": generationContext.Definitions,
		"startCandidateDefinitionVersionIds": candidateIDs,
		"workbenchTargets":                   generationContext.WorkbenchTargets,
		"exactSourceRefs":                    validationInput.SourceRefs, "manifestIntent": validationInput.ManifestIntent,
	})
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	schema, err := intentProposalSchema(candidateIDs, generationContext.WorkbenchTargets)
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	generated, err := s.provider.Generate(ctx, ai.Request{
		RunID: input.TriggerMessageID, Model: strings.TrimSpace(input.Model),
		Instructions: "Return a reviewable intent proposal only. Never invent an ID or claim that a workflow, artifact, bundle, or application was changed. Preserve the supplied exact source and manifest identities. For start_workflow, select only an ID from startCandidateDefinitionVersionIds. For workbench_instruction, select one supplied workbenchTargets entry exactly: use its definitionVersionId and runId, and use its rootBundleId as expectedBundleId. If workbenchTargets is empty, choose start_workflow. Do not add a conversationIntent property to scope; the server injects that reserved reviewed instruction envelope.",
		Input:        providerInput, OutputSchema: schema, OutputSchemaName: "workflow_intent_proposal", MaxOutputTokens: 8192,
	})
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	var output struct {
		AssistantContent             string               `json:"assistantContent"`
		Kind                         IntentKind           `json:"kind"`
		SuggestedDefinitionVersionID string               `json:"suggestedDefinitionVersionId"`
		Scope                        json.RawMessage      `json:"scope"`
		WorkbenchInstruction         WorkbenchInstruction `json:"workbenchInstruction"`
	}
	if err := json.Unmarshal(generated.Output, &output); err != nil {
		return GeneratedIntentProposal{}, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	switch output.Kind {
	case IntentStartWorkflow:
		if _, allowed := candidateSet[output.SuggestedDefinitionVersionID]; !allowed ||
			output.WorkbenchInstruction.ExpectedRunID != "" || output.WorkbenchInstruction.ExpectedBundleID != "" {
			return GeneratedIntentProposal{}, ai.ErrInvalidOutput
		}
	case IntentWorkbenchInstruction:
		matched := false
		for _, target := range generationContext.WorkbenchTargets {
			if output.SuggestedDefinitionVersionID == target.DefinitionVersionID &&
				output.WorkbenchInstruction.ExpectedRunID == target.RunID &&
				output.WorkbenchInstruction.ExpectedBundleID == target.RootBundleID {
				matched = true
				break
			}
		}
		if !matched {
			return GeneratedIntentProposal{}, ai.ErrInvalidOutput
		}
	default:
		return GeneratedIntentProposal{}, ai.ErrInvalidOutput
	}
	proposal, message, err := s.createIntentProposal(ctx, projectID, conversationID, actorID, CreateIntentProposalInput{
		TriggerMessageID: input.TriggerMessageID, AssistantContent: output.AssistantContent,
		Kind: output.Kind, SuggestedDefinitionVersionID: output.SuggestedDefinitionVersionID,
		Scope: output.Scope, SourceRefs: validationInput.SourceRefs, ManifestIntent: validationInput.ManifestIntent,
		WorkbenchInstruction: output.WorkbenchInstruction,
	}, ProposalProvenance{Origin: ProposalOriginAI, AI: &AIProvenance{
		Provider: generated.Provider, Model: generated.Model, ResponseID: generated.ResponseID,
	}})
	if err != nil {
		return GeneratedIntentProposal{}, err
	}
	return GeneratedIntentProposal{Proposal: proposal, Message: message, Provider: generated.Provider, Model: generated.Model}, nil
}

func (s *Service) GetProposal(ctx context.Context, projectID, conversationID, proposalID, actorID string) (WorkflowIntentProposal, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return WorkflowIntentProposal{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return WorkflowIntentProposal{}, err
	}
	proposalUUID, err := parseID(proposalID, "proposal id")
	if err != nil {
		return WorkflowIntentProposal{}, err
	}
	return s.store.GetProposal(ctx, projectUUID, conversationUUID, proposalUUID)
}

func (s *Service) ListProposals(ctx context.Context, projectID, conversationID, actorID string, options ListOptions) (ProposalPage, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return ProposalPage{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return ProposalPage{}, err
	}
	options, err = normalizeListOptions(options)
	if err != nil {
		return ProposalPage{}, err
	}
	return s.store.ListProposals(ctx, projectUUID, conversationUUID, options)
}

func (s *Service) DecideProposal(ctx context.Context, projectID, conversationID, proposalID, actorID, expectedETag string, input DecideProposalInput) (WorkflowIntentProposal, *ConversationCommand, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return WorkflowIntentProposal{}, nil, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return WorkflowIntentProposal{}, nil, err
	}
	proposalUUID, err := parseID(proposalID, "proposal id")
	if err != nil {
		return WorkflowIntentProposal{}, nil, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if strings.TrimSpace(expectedETag) == "" || input.Decision != DecisionAccept && input.Decision != DecisionReject || len(input.Reason) > 2000 {
		return WorkflowIntentProposal{}, nil, fmt.Errorf("%w: proposal decision", core.ErrInvalidInput)
	}
	return s.store.DecideProposal(ctx, projectUUID, conversationUUID, proposalUUID, actorUUID, expectedETag, input)
}

func (s *Service) GetCommand(ctx context.Context, projectID, conversationID, commandID, actorID string) (ConversationCommand, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return ConversationCommand{}, err
	}
	conversationUUID, commandUUID, err := parseConversationCommandIDs(conversationID, commandID)
	if err != nil {
		return ConversationCommand{}, err
	}
	return s.store.GetCommand(ctx, projectUUID, conversationUUID, commandUUID)
}

func (s *Service) ListCommands(ctx context.Context, projectID, conversationID, actorID string, options ListOptions) (CommandPage, error) {
	projectUUID, _, err := s.authorize(ctx, projectID, actorID, core.ActionView)
	if err != nil {
		return CommandPage{}, err
	}
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return CommandPage{}, err
	}
	options, err = normalizeListOptions(options)
	if err != nil {
		return CommandPage{}, err
	}
	return s.store.ListCommands(ctx, projectUUID, conversationUUID, options)
}

func (s *Service) ExecuteCommand(ctx context.Context, projectID, conversationID, commandID, actorID, expectedETag string, input ExecuteCommandInput) (ConversationCommand, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return ConversationCommand{}, err
	}
	conversationUUID, commandUUID, err := parseConversationCommandIDs(conversationID, commandID)
	if err != nil {
		return ConversationCommand{}, err
	}
	if strings.TrimSpace(expectedETag) == "" {
		return ConversationCommand{}, fmt.Errorf("%w: command precondition", core.ErrInvalidInput)
	}
	current, err := s.store.GetCommand(ctx, projectUUID, conversationUUID, commandUUID)
	if err != nil {
		return ConversationCommand{}, err
	}
	switch current.Kind {
	case IntentStartWorkflow:
		if input.WorkbenchResult != nil {
			return ConversationCommand{}, fmt.Errorf("%w: start_workflow command has no client result", core.ErrInvalidInput)
		}
	case IntentWorkbenchInstruction:
		if input.WorkbenchResult == nil {
			return ConversationCommand{}, fmt.Errorf("%w: workbench result", core.ErrInvalidInput)
		}
		return s.store.CompleteWorkbenchCommand(
			ctx, projectUUID, conversationUUID, commandUUID, actorUUID, expectedETag, s.claimLease, *input.WorkbenchResult,
		)
	default:
		return ConversationCommand{}, fmt.Errorf("%w: command kind", core.ErrInvalidInput)
	}
	claim, err := s.store.ClaimCommand(ctx, projectUUID, conversationUUID, commandUUID, actorUUID, expectedETag, s.claimLease)
	if err != nil {
		return ConversationCommand{}, err
	}
	writebackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	run, startErr := s.workflow.Start(ctx, projectID, actorID, runtime.StartRequest{
		RunID: claim.Command.ID, DefinitionVersionID: claim.Command.Payload.DefinitionVersionID,
		InputManifest: claim.Command.Payload.ManifestIntent.InputManifest, Scope: claim.Command.Payload.Scope,
	})
	if startErr != nil {
		recovered, recoverErr := s.workflow.GetRun(writebackCtx, projectID, claim.Command.ID, actorID)
		if recoverErr == nil && runMatchesCommand(recovered, claim.Command, actorID) {
			run = recovered
			startErr = nil
		}
	}
	if startErr != nil {
		return s.store.CompleteCommand(writebackCtx, claim, actorUUID, CommandFailed, nil, &CommandFailure{
			Code: "workflow_start_failed", Message: "The approved workflow command could not be started.",
		})
	}
	if !runMatchesCommand(run, claim.Command, actorID) {
		return s.store.CompleteCommand(writebackCtx, claim, actorUUID, CommandFailed, nil, &CommandFailure{
			Code: "workflow_identity_mismatch", Message: "The workflow runtime did not return the exact approved run identity.",
		})
	}
	result, _ := platformdomain.CanonicalJSON(map[string]any{
		"runId": run.ID, "definitionVersionId": run.DefinitionVersionID, "inputManifest": run.InputManifest,
	})
	return s.store.CompleteCommand(writebackCtx, claim, actorUUID, CommandExecuted, result, nil)
}

func (s *Service) RejectCommand(ctx context.Context, projectID, conversationID, commandID, actorID, expectedETag string, input RejectCommandInput) (ConversationCommand, error) {
	projectUUID, actorUUID, err := s.authorize(ctx, projectID, actorID, core.ActionEdit)
	if err != nil {
		return ConversationCommand{}, err
	}
	conversationUUID, commandUUID, err := parseConversationCommandIDs(conversationID, commandID)
	if err != nil {
		return ConversationCommand{}, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if strings.TrimSpace(expectedETag) == "" || input.Reason == "" || len(input.Reason) > 2000 {
		return ConversationCommand{}, fmt.Errorf("%w: command rejection", core.ErrInvalidInput)
	}
	return s.store.RejectCommand(ctx, projectUUID, conversationUUID, commandUUID, actorUUID, expectedETag, input.Reason)
}

func (s *Service) authorize(ctx context.Context, projectID, actorID string, action core.Action) (uuid.UUID, uuid.UUID, error) {
	projectUUID, err := parseID(projectID, "project id")
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	actorUUID, err := parseID(actorID, "actor id")
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if _, err := s.access.Authorize(ctx, projectUUID.String(), actorUUID.String(), action); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return projectUUID, actorUUID, nil
}

func parseID(value, field string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %s", core.ErrInvalidInput, field)
	}
	return parsed, nil
}

func parseConversationCommandIDs(conversationID, commandID string) (uuid.UUID, uuid.UUID, error) {
	conversationUUID, err := parseID(conversationID, "conversation id")
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	commandUUID, err := parseID(commandID, "command id")
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return conversationUUID, commandUUID, nil
}

func runMatchesCommand(run *runtime.RunRecord, command ConversationCommand, actorID string) bool {
	if run == nil || run.ID != command.ID || run.ProjectID != command.ProjectID ||
		run.DefinitionVersionID != command.Payload.DefinitionVersionID || run.StartedBy != actorID || run.InputManifest == nil ||
		*run.InputManifest != command.Payload.ManifestIntent.InputManifest {
		return false
	}
	expectedHash, expectedErr := platformdomain.CanonicalHash(command.Payload.Scope)
	actualHash, actualErr := platformdomain.CanonicalHash(run.Scope)
	return expectedErr == nil && actualErr == nil && expectedHash == actualHash
}

func (s *Service) validateManifestSources(ctx context.Context, projectID string, intent ManifestIntent, refs []platformdomain.ArtifactRef) (platformdomain.InputManifest, error) {
	manifest, err := s.manifests.GetManifest(ctx, intent.InputManifest.ID)
	if err != nil {
		return platformdomain.InputManifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return platformdomain.InputManifest{}, err
	}
	if manifest.ProjectID != projectID || manifest.Ref() != intent.InputManifest {
		return platformdomain.InputManifest{}, platformdomain.ErrManifestUnpinned
	}
	expected := make(map[string]struct{}, len(manifest.Sources)+1)
	if manifest.BaseRevision != nil {
		expected[artifactRefKey(*manifest.BaseRevision)] = struct{}{}
	}
	for _, source := range manifest.Sources {
		expected[artifactRefKey(source.Ref)] = struct{}{}
	}
	actual := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		actual[artifactRefKey(ref)] = struct{}{}
	}
	if len(expected) != len(actual) {
		return platformdomain.InputManifest{}, platformdomain.ErrManifestUnpinned
	}
	for key := range expected {
		if _, exists := actual[key]; !exists {
			return platformdomain.InputManifest{}, platformdomain.ErrManifestUnpinned
		}
	}
	return manifest, nil
}

func validateLoadedStartCandidateCompatibility(
	manifestJobType string,
	candidateIDs map[string]struct{},
	definitions []intentDefinitionContext,
) error {
	seen := make(map[string]struct{}, len(candidateIDs))
	for _, definition := range definitions {
		if _, candidate := candidateIDs[definition.VersionID]; !candidate {
			// Definition contexts for active Workbench runs are deliberately not
			// start candidates. Their M0 input remains independent of the current
			// M1 conversation-governance manifest.
			continue
		}
		if _, duplicate := seen[definition.VersionID]; duplicate {
			return fmt.Errorf("%w: duplicate candidate workflow definition", core.ErrInvalidInput)
		}
		if err := validateStartDefinitionCompatibility(manifestJobType, definition.Content); err != nil {
			return err
		}
		seen[definition.VersionID] = struct{}{}
	}
	if len(seen) != len(candidateIDs) {
		return core.ErrNotFound
	}
	return nil
}

func artifactRefKey(ref platformdomain.ArtifactRef) string {
	return ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" + ref.AnchorID
}

func normalizeProposalProvenance(value ProposalProvenance) (ProposalProvenance, error) {
	switch value.Origin {
	case ProposalOriginSubmitted:
		if value.AI != nil {
			return ProposalProvenance{}, fmt.Errorf("%w: submitted proposal provenance", core.ErrInvalidInput)
		}
	case ProposalOriginAI:
		if value.AI == nil {
			return ProposalProvenance{}, fmt.Errorf("%w: AI proposal provenance", core.ErrInvalidInput)
		}
		value.AI.Provider = strings.TrimSpace(value.AI.Provider)
		value.AI.Model = strings.TrimSpace(value.AI.Model)
		value.AI.ResponseID = strings.TrimSpace(value.AI.ResponseID)
		if value.AI.Provider == "" || value.AI.Model == "" || len(value.AI.Provider) > 256 || len(value.AI.Model) > 256 || len(value.AI.ResponseID) > 512 {
			return ProposalProvenance{}, fmt.Errorf("%w: AI proposal provenance", core.ErrInvalidInput)
		}
	default:
		return ProposalProvenance{}, fmt.Errorf("%w: proposal origin", core.ErrInvalidInput)
	}
	return value, nil
}

func intentProposalSchema(candidateIDs []string, targets []intentWorkbenchTargetContext) (json.RawMessage, error) {
	definitionIDs := append([]string(nil), candidateIDs...)
	seenDefinitionIDs := make(map[string]struct{}, len(candidateIDs)+len(targets))
	for _, definitionID := range candidateIDs {
		seenDefinitionIDs[definitionID] = struct{}{}
	}
	branches := []any{map[string]any{
		"properties": map[string]any{
			"kind":                         map[string]any{"const": string(IntentStartWorkflow)},
			"suggestedDefinitionVersionId": map[string]any{"enum": candidateIDs},
			"workbenchInstruction": map[string]any{
				"not": map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"expectedRunId"}},
					map[string]any{"required": []string{"expectedBundleId"}},
				}},
			},
		},
	}}
	for _, target := range targets {
		if _, exists := seenDefinitionIDs[target.DefinitionVersionID]; !exists {
			seenDefinitionIDs[target.DefinitionVersionID] = struct{}{}
			definitionIDs = append(definitionIDs, target.DefinitionVersionID)
		}
		branches = append(branches, map[string]any{
			"properties": map[string]any{
				"kind":                         map[string]any{"const": string(IntentWorkbenchInstruction)},
				"suggestedDefinitionVersionId": map[string]any{"const": target.DefinitionVersionID},
				"workbenchInstruction": map[string]any{
					"required": []string{"expectedRunId", "expectedBundleId"},
					"properties": map[string]any{
						"expectedRunId":    map[string]any{"const": target.RunID},
						"expectedBundleId": map[string]any{"const": target.RootBundleID},
					},
				},
			},
		})
	}
	kinds := []string{string(IntentStartWorkflow)}
	if len(targets) != 0 {
		kinds = append(kinds, string(IntentWorkbenchInstruction))
	}
	return platformdomain.CanonicalJSON(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object", "additionalProperties": false,
		"required": []string{"assistantContent", "kind", "suggestedDefinitionVersionId", "scope", "workbenchInstruction"},
		"oneOf":    branches,
		"properties": map[string]any{
			"assistantContent":             map[string]any{"type": "string", "minLength": 1, "maxLength": 32768},
			"kind":                         map[string]any{"type": "string", "enum": kinds},
			"suggestedDefinitionVersionId": map[string]any{"type": "string", "enum": definitionIDs},
			"scope":                        map[string]any{"type": "object", "maxProperties": 100},
			"workbenchInstruction": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"objective", "constraints"},
				"properties": map[string]any{
					"objective":        map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
					"constraints":      map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 1000}},
					"expectedRunId":    map[string]any{"type": "string"},
					"expectedBundleId": map[string]any{"type": "string"},
				},
			},
		},
	})
}
