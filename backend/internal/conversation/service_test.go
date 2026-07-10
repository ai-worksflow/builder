package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
)

type conversationAccessStub struct {
	err     error
	actions []core.Action
	mu      sync.Mutex
}

func (s *conversationAccessStub) Authorize(_ context.Context, _, _ string, action core.Action) (core.Role, error) {
	s.mu.Lock()
	s.actions = append(s.actions, action)
	s.mu.Unlock()
	if s.err != nil {
		return core.RoleViewer, s.err
	}
	return core.RoleEditor, nil
}

type conversationProviderStub struct{}

func (conversationProviderStub) Generate(context.Context, ai.Request) (ai.Result, error) {
	return ai.Result{}, ai.ErrNotConfigured
}

type generatedConversationProviderStub struct {
	result  ai.Result
	request ai.Request
	calls   int
}

func (s *generatedConversationProviderStub) Generate(_ context.Context, request ai.Request) (ai.Result, error) {
	s.calls++
	s.request = request
	return s.result, nil
}

type conversationManifestStub struct {
	manifest platformdomain.InputManifest
	err      error
}

func (s conversationManifestStub) GetManifest(context.Context, string) (platformdomain.InputManifest, error) {
	return s.manifest, s.err
}

type conversationRuntimeStub struct {
	starts atomic.Int64
	mu     sync.Mutex
	last   runtime.StartRequest
}

func (s *conversationRuntimeStub) Start(_ context.Context, projectID, actorID string, request runtime.StartRequest) (*runtime.RunRecord, error) {
	s.starts.Add(1)
	s.mu.Lock()
	s.last = request
	s.mu.Unlock()
	manifest := request.InputManifest
	return &runtime.RunRecord{
		ID: request.RunID, ProjectID: projectID, DefinitionVersionID: request.DefinitionVersionID,
		InputManifest: &manifest, Scope: append(json.RawMessage(nil), request.Scope...), StartedBy: actorID,
	}, nil
}

func (s *conversationRuntimeStub) GetRun(context.Context, string, string, string) (*runtime.RunRecord, error) {
	return nil, core.ErrNotFound
}

type conversationStoreStub struct {
	mu                   sync.Mutex
	appendCalls          int
	proposalAccepted     bool
	command              ConversationCommand
	claimToken           *uuid.UUID
	proposal             WorkflowIntentProposal
	generationContext    intentGenerationContext
	claimCalls           int
	atomicWorkbenchCalls int
}

func (s *conversationStoreStub) CreateConversation(context.Context, uuid.UUID, uuid.UUID, string) (Conversation, error) {
	return Conversation{}, nil
}
func (s *conversationStoreStub) GetConversation(context.Context, uuid.UUID, uuid.UUID) (Conversation, error) {
	return Conversation{}, nil
}
func (s *conversationStoreStub) ListConversations(context.Context, uuid.UUID, ListOptions) (ConversationPage, error) {
	return ConversationPage{}, nil
}
func (s *conversationStoreStub) UpdateConversation(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, UpdateConversationInput) (Conversation, error) {
	return Conversation{}, nil
}
func (s *conversationStoreStub) AppendUserMessage(_ context.Context, _, _, _ uuid.UUID, content string) (Message, error) {
	s.mu.Lock()
	s.appendCalls++
	s.mu.Unlock()
	return Message{ID: uuid.NewString(), Content: content, Role: MessageUser}, nil
}
func (s *conversationStoreStub) ListMessages(context.Context, uuid.UUID, uuid.UUID, ListOptions) (MessagePage, error) {
	return MessagePage{}, nil
}
func (s *conversationStoreStub) CreateIntentProposal(_ context.Context, _, conversationID, actorID uuid.UUID, input CreateIntentProposalInput, provenance ProposalProvenance, _ string) (WorkflowIntentProposal, Message, error) {
	proposalID, messageID := uuid.NewString(), uuid.NewString()
	proposal := WorkflowIntentProposal{
		ID: proposalID, ConversationID: conversationID.String(), TriggerMessageID: input.TriggerMessageID,
		AssistantMessageID: messageID, Kind: input.Kind, Status: ProposalPending, Version: 1,
		ETag: ProposalETag(proposalID, 1), SuggestedDefinitionVersionID: input.SuggestedDefinitionVersionID,
		Scope: input.Scope, SourceRefs: input.SourceRefs, ManifestIntent: input.ManifestIntent,
		WorkbenchInstruction: input.WorkbenchInstruction, Origin: provenance.Origin, AI: provenance.AI, ProposedBy: actorID.String(),
	}
	s.mu.Lock()
	s.proposal = proposal
	s.mu.Unlock()
	return proposal, Message{ID: messageID, ConversationID: conversationID.String(), Role: MessageAssistant, ProposalID: &proposalID}, nil
}
func (s *conversationStoreStub) IntentGenerationContext(_ context.Context, _, _, _ uuid.UUID, candidateIDs []uuid.UUID, _ []platformdomain.ArtifactRef, _ ManifestIntent, _ string) (intentGenerationContext, error) {
	result := s.generationContext
	result.Definitions = append([]intentDefinitionContext(nil), result.Definitions...)
	seen := make(map[string]struct{}, len(result.Definitions))
	for _, definition := range result.Definitions {
		seen[definition.VersionID] = struct{}{}
	}
	for _, candidateID := range candidateIDs {
		if _, exists := seen[candidateID.String()]; exists {
			continue
		}
		result.Definitions = append(result.Definitions, intentDefinitionContext{
			VersionID: candidateID.String(), Content: json.RawMessage(`{"nodes":[],"edges":[]}`),
		})
	}
	return result, nil
}
func (s *conversationStoreStub) GetProposal(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (WorkflowIntentProposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proposal, nil
}
func (s *conversationStoreStub) ListProposals(context.Context, uuid.UUID, uuid.UUID, ListOptions) (ProposalPage, error) {
	return ProposalPage{}, nil
}
func (s *conversationStoreStub) DecideProposal(_ context.Context, _, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, _ string, input DecideProposalInput) (WorkflowIntentProposal, *ConversationCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proposal := s.proposal
	proposal.Version++
	proposal.ETag = ProposalETag(proposal.ID, proposal.Version)
	if input.Decision == DecisionReject {
		proposal.Status = ProposalRejected
		s.proposal = proposal
		return proposal, nil, nil
	}
	proposal.Status = ProposalAccepted
	s.proposal = proposal
	command := s.command
	if command.ID == "" {
		command = conversationCommandFixture()
	}
	s.command = command
	s.proposalAccepted = true
	return proposal, &command, nil
}
func (s *conversationStoreStub) GetCommand(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (ConversationCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneCommand(s.command), nil
}
func (s *conversationStoreStub) ListCommands(context.Context, uuid.UUID, uuid.UUID, ListOptions) (CommandPage, error) {
	return CommandPage{}, nil
}
func (s *conversationStoreStub) ClaimCommand(_ context.Context, _, _, _ uuid.UUID, actorID uuid.UUID, expectedETag string, _ time.Duration) (commandClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimCalls++
	if !s.proposalAccepted || s.command.Status != CommandPending || s.command.ETag != expectedETag || s.claimToken != nil {
		return commandClaim{}, core.ErrConflict
	}
	token := uuid.New()
	s.claimToken = &token
	s.command.Version++
	s.command.ETag = CommandETag(s.command.ID, s.command.Version)
	actor := actorID.String()
	s.command.ExecutionActorID = &actor
	return commandClaim{Command: cloneCommand(s.command), Token: token}, nil
}
func (s *conversationStoreStub) CompleteWorkbenchCommand(_ context.Context, _, _, _ uuid.UUID, actorID uuid.UUID, expectedETag string, _ time.Duration, result WorkbenchExecutionResult) (ConversationCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.atomicWorkbenchCalls++
	if !s.proposalAccepted || s.command.Kind != IntentWorkbenchInstruction || s.command.Status != CommandPending || s.command.ETag != expectedETag {
		return ConversationCommand{}, core.ErrConflict
	}
	encoded, err := platformdomain.CanonicalJSON(result)
	if err != nil {
		return ConversationCommand{}, err
	}
	now := time.Now().UTC()
	s.command.Status = CommandExecuted
	s.command.Version += 2
	s.command.ETag = CommandETag(s.command.ID, s.command.Version)
	s.command.Result = encoded
	actor := actorID.String()
	s.command.ExecutionActorID = &actor
	s.command.ExecutedBy = &actor
	s.command.ExecutedAt = &now
	return cloneCommand(s.command), nil
}
func (s *conversationStoreStub) CompleteCommand(_ context.Context, claim commandClaim, actorID uuid.UUID, status CommandStatus, result json.RawMessage, failure *CommandFailure) (ConversationCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimToken == nil || *s.claimToken != claim.Token || status != CommandExecuted && status != CommandFailed {
		return ConversationCommand{}, core.ErrConflict
	}
	s.command.Status = status
	s.command.Version++
	s.command.ETag = CommandETag(s.command.ID, s.command.Version)
	s.command.Result = append(json.RawMessage(nil), result...)
	s.command.Failure = failure
	actor := actorID.String()
	s.command.ExecutedBy = &actor
	s.claimToken = nil
	return cloneCommand(s.command), nil
}
func (s *conversationStoreStub) RejectCommand(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, string) (ConversationCommand, error) {
	return ConversationCommand{}, nil
}
func (s *conversationStoreStub) ValidateWorkbenchResult(context.Context, uuid.UUID, CommandPayload, WorkbenchExecutionResult) error {
	return nil
}

func TestConversationServiceEnforcesProjectPermissionBeforePersistence(t *testing.T) {
	store := &conversationStoreStub{}
	access := &conversationAccessStub{err: core.ErrForbidden}
	service, err := newService(store, access, &conversationRuntimeStub{}, conversationManifestStub{}, conversationProviderStub{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AppendMessage(context.Background(), uuid.NewString(), uuid.NewString(), uuid.NewString(), AppendMessageInput{Content: "private requirement"})
	if !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("permission error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.appendCalls != 0 {
		t.Fatal("forbidden request reached conversation persistence")
	}
	access.mu.Lock()
	defer access.mu.Unlock()
	if len(access.actions) != 1 || access.actions[0] != core.ActionComment {
		t.Fatalf("message permission action = %v", access.actions)
	}
}

func TestAssistantOutputRemainsPendingProposalUntilHumanDecision(t *testing.T) {
	store := &conversationStoreStub{}
	runtimeStub := &conversationRuntimeStub{}
	service, err := newService(store, &conversationAccessStub{}, runtimeStub, conversationManifestStub{}, conversationProviderStub{})
	if err != nil {
		t.Fatal(err)
	}
	projectID, conversationID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	input, manifest := proposalInputWithManifest(t, projectID, actorID)
	service.manifests = conversationManifestStub{manifest: manifest}
	proposal, message, err := service.CreateIntentProposal(context.Background(), projectID, conversationID, actorID, input)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status != ProposalPending || proposal.Origin != ProposalOriginSubmitted || proposal.AI != nil || message.Role != MessageAssistant || message.ProposalID == nil || *message.ProposalID != proposal.ID {
		t.Fatalf("assistant output was not bound to a pending proposal: proposal=%+v message=%+v", proposal, message)
	}
	if runtimeStub.starts.Load() != 0 {
		t.Fatal("creating an assistant proposal executed business workflow")
	}
	decided, command, err := service.DecideProposal(context.Background(), projectID, conversationID, proposal.ID, actorID, proposal.ETag, DecideProposalInput{Decision: DecisionReject, Reason: "not yet"})
	if err != nil || decided.Status != ProposalRejected || command != nil {
		t.Fatalf("rejected proposal created a command: proposal=%+v command=%+v err=%v", decided, command, err)
	}
	if runtimeStub.starts.Load() != 0 {
		t.Fatal("rejecting a proposal executed business workflow")
	}
}

func TestServerAIGenerationPersistsReviewableProvenanceWithoutExecution(t *testing.T) {
	store := &conversationStoreStub{}
	runtimeStub := &conversationRuntimeStub{}
	projectID, conversationID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	input, manifest := proposalInputWithManifest(t, projectID, actorID)
	output, err := platformdomain.CanonicalJSON(map[string]any{
		"assistantContent": "I recommend the minimum loop for review.",
		"kind":             IntentStartWorkflow, "suggestedDefinitionVersionId": input.SuggestedDefinitionVersionID,
		"scope":                map[string]any{"slice": "all"},
		"workbenchInstruction": map[string]any{"objective": "Build the reviewed application", "constraints": []string{"Keep exact traces"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &generatedConversationProviderStub{result: ai.Result{
		Provider: "test-provider", Model: "test-model", ResponseID: "response-1", Output: output,
	}}
	service, err := newService(store, &conversationAccessStub{}, runtimeStub, conversationManifestStub{manifest: manifest}, provider)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.GenerateIntentProposal(context.Background(), projectID, conversationID, actorID, GenerateIntentProposalInput{
		TriggerMessageID: input.TriggerMessageID, CandidateDefinitionVersionIDs: []string{input.SuggestedDefinitionVersionID},
		SourceRefs: input.SourceRefs, ManifestIntent: input.ManifestIntent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Proposal.Status != ProposalPending || result.Proposal.Origin != ProposalOriginAI || result.Proposal.AI == nil ||
		result.Proposal.AI.Provider != "test-provider" || result.Proposal.AI.Model != "test-model" || result.Proposal.AI.ResponseID != "response-1" ||
		result.Message.Role != MessageAssistant || result.Provider != "test-provider" || result.Model != "test-model" {
		t.Fatalf("AI provenance or review boundary was lost: %+v", result)
	}
	if runtimeStub.starts.Load() != 0 {
		t.Fatal("server AI intent generation executed business workflow")
	}
	if len(provider.request.OutputSchema) == 0 || provider.request.OutputSchemaName != "workflow_intent_proposal" {
		t.Fatalf("AI generation was not schema constrained: %+v", provider.request)
	}
	if bytes.Contains(provider.request.OutputSchema, []byte(string(IntentWorkbenchInstruction))) {
		t.Fatalf("schema offered workbench_instruction without an authoritative target: %s", provider.request.OutputSchema)
	}
}

func TestServerAIGenerationRejectsBlueprintSelectionStartCandidateForConversationManifest(t *testing.T) {
	projectID, conversationID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	input, manifest := proposalInputWithManifest(t, projectID, actorID)
	selectionContent := json.RawMessage(`{"nodes":[{"id":"pages","type":"fan_out","fanOut":{"itemKind":"blueprint_selection_page"}}],"edges":[]}`)
	store := &conversationStoreStub{generationContext: intentGenerationContext{Definitions: []intentDefinitionContext{{
		VersionID: input.SuggestedDefinitionVersionID, Content: selectionContent,
	}}}}
	provider := &generatedConversationProviderStub{}
	service, err := newService(store, &conversationAccessStub{}, &conversationRuntimeStub{}, conversationManifestStub{manifest: manifest}, provider)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GenerateIntentProposal(context.Background(), projectID, conversationID, actorID, GenerateIntentProposalInput{
		TriggerMessageID: input.TriggerMessageID, CandidateDefinitionVersionIDs: []string{input.SuggestedDefinitionVersionID},
		SourceRefs: input.SourceRefs, ManifestIntent: input.ManifestIntent,
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("incompatible selection start candidate error = %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("incompatible start candidate reached AI provider %d times", provider.calls)
	}
}

func TestServerAIGenerationUsesOnlySuppliedAuthoritativeWorkbenchTarget(t *testing.T) {
	projectID, conversationID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	input, manifest := proposalInputWithManifest(t, projectID, actorID)
	target := intentWorkbenchTargetContext{
		DefinitionVersionID: uuid.NewString(), RunID: uuid.NewString(),
		RootBundleID: uuid.NewString(), ActiveBundleID: uuid.NewString(),
		ManifestGroup: uuid.NewString(), Ordinal: 2,
	}
	output, err := platformdomain.CanonicalJSON(map[string]any{
		"assistantContent": "Continue the existing reviewed application run.",
		"kind":             IntentWorkbenchInstruction, "suggestedDefinitionVersionId": target.DefinitionVersionID,
		"scope": map[string]any{"slice": "all"},
		"workbenchInstruction": map[string]any{
			"objective": "Continue the exact active bundle", "constraints": []string{"Keep exact traces"},
			"expectedRunId": target.RunID, "expectedBundleId": target.RootBundleID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &conversationStoreStub{generationContext: intentGenerationContext{
		Definitions: []intentDefinitionContext{{
			VersionID: target.DefinitionVersionID,
			Content:   json.RawMessage(`{"nodes":[{"id":"pages","type":"fan_out","fanOut":{"itemKind":"blueprint_selection_page"}}],"edges":[]}`),
		}},
		WorkbenchTargets: []intentWorkbenchTargetContext{target},
	}}
	provider := &generatedConversationProviderStub{result: ai.Result{Provider: "test", Model: "test", Output: output}}
	service, err := newService(store, &conversationAccessStub{}, &conversationRuntimeStub{}, conversationManifestStub{manifest: manifest}, provider)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := service.GenerateIntentProposal(context.Background(), projectID, conversationID, actorID, GenerateIntentProposalInput{
		TriggerMessageID: input.TriggerMessageID, CandidateDefinitionVersionIDs: []string{input.SuggestedDefinitionVersionID},
		SourceRefs: input.SourceRefs, ManifestIntent: input.ManifestIntent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated.Proposal.Kind != IntentWorkbenchInstruction || generated.Proposal.SuggestedDefinitionVersionID != target.DefinitionVersionID ||
		generated.Proposal.WorkbenchInstruction.ExpectedRunID != target.RunID || generated.Proposal.WorkbenchInstruction.ExpectedBundleID != target.RootBundleID {
		t.Fatalf("AI proposal lost the supplied target: %+v", generated.Proposal)
	}
	for _, exactID := range []string{target.DefinitionVersionID, target.RunID, target.RootBundleID, target.ActiveBundleID} {
		if !bytes.Contains(provider.request.Input, []byte(exactID)) || !bytes.Contains(provider.request.OutputSchema, []byte(exactID)) && exactID != target.ActiveBundleID {
			t.Fatalf("provider request omitted authoritative target identity %s", exactID)
		}
	}
	var prompt struct {
		StartCandidateDefinitionVersionIDs []string `json:"startCandidateDefinitionVersionIds"`
	}
	if err := json.Unmarshal(provider.request.Input, &prompt); err != nil {
		t.Fatal(err)
	}
	if len(prompt.StartCandidateDefinitionVersionIDs) != 1 || prompt.StartCandidateDefinitionVersionIDs[0] != input.SuggestedDefinitionVersionID {
		t.Fatalf("active selection target leaked into start candidates: %+v", prompt.StartCandidateDefinitionVersionIDs)
	}

	commandID := uuid.NewString()
	store.command = ConversationCommand{
		ID: commandID, ProjectID: projectID, ConversationID: conversationID, ProposalID: generated.Proposal.ID,
		Kind: IntentWorkbenchInstruction, Status: CommandPending, Version: 1, ETag: CommandETag(commandID, 1),
		Payload: CommandPayload{
			DefinitionVersionID: target.DefinitionVersionID, Scope: generated.Proposal.Scope,
			SourceRefs: generated.Proposal.SourceRefs, ManifestIntent: generated.Proposal.ManifestIntent,
			Workbench: generated.Proposal.WorkbenchInstruction,
		},
		AcceptedBy: actorID,
	}
	accepted, command, err := service.DecideProposal(
		context.Background(), projectID, conversationID, generated.Proposal.ID, actorID, generated.Proposal.ETag,
		DecideProposalInput{Decision: DecisionAccept},
	)
	if err != nil || command == nil || accepted.Status != ProposalAccepted || command.Kind != IntentWorkbenchInstruction {
		t.Fatalf("accept active selection Workbench target: proposal=%+v command=%+v err=%v", accepted, command, err)
	}
	executed, err := service.ExecuteCommand(
		context.Background(), projectID, conversationID, command.ID, actorID, command.ETag,
		ExecuteCommandInput{WorkbenchResult: &WorkbenchExecutionResult{
			RunID: target.RunID, BundleID: target.ActiveBundleID, ImplementationProposalID: uuid.NewString(),
		}},
	)
	if err != nil || executed.Status != CommandExecuted || store.atomicWorkbenchCalls != 1 {
		t.Fatalf("execute active selection Workbench target: command=%+v atomic=%d err=%v", executed, store.atomicWorkbenchCalls, err)
	}
	if starts := service.workflow.(*conversationRuntimeStub).starts.Load(); starts != 0 {
		t.Fatalf("Workbench continuation incorrectly started a new workflow %d times", starts)
	}

	selectionStartOutput, _ := platformdomain.CanonicalJSON(map[string]any{
		"assistantContent": "Start the selection definition incorrectly.",
		"kind":             IntentStartWorkflow, "suggestedDefinitionVersionId": target.DefinitionVersionID,
		"scope": map[string]any{},
		"workbenchInstruction": map[string]any{
			"objective": "Start selection", "constraints": []string{},
		},
	})
	provider.result.Output = selectionStartOutput
	if _, err := service.GenerateIntentProposal(context.Background(), projectID, conversationID, actorID, GenerateIntentProposalInput{
		TriggerMessageID: input.TriggerMessageID, CandidateDefinitionVersionIDs: []string{input.SuggestedDefinitionVersionID},
		SourceRefs: input.SourceRefs, ManifestIntent: input.ManifestIntent,
	}); !errors.Is(err, ai.ErrInvalidOutput) {
		t.Fatalf("selection target was accepted as a start candidate: %v", err)
	}

	hallucinated := target
	hallucinated.RunID = uuid.NewString()
	hallucinatedOutput, _ := platformdomain.CanonicalJSON(map[string]any{
		"assistantContent": "Continue an invented run.",
		"kind":             IntentWorkbenchInstruction, "suggestedDefinitionVersionId": target.DefinitionVersionID,
		"scope": map[string]any{},
		"workbenchInstruction": map[string]any{
			"objective": "Continue", "constraints": []string{},
			"expectedRunId": hallucinated.RunID, "expectedBundleId": target.RootBundleID,
		},
	})
	provider.result.Output = hallucinatedOutput
	if _, err := service.GenerateIntentProposal(context.Background(), projectID, conversationID, actorID, GenerateIntentProposalInput{
		TriggerMessageID: input.TriggerMessageID, CandidateDefinitionVersionIDs: []string{input.SuggestedDefinitionVersionID},
		SourceRefs: input.SourceRefs, ManifestIntent: input.ManifestIntent,
	}); !errors.Is(err, ai.ErrInvalidOutput) {
		t.Fatalf("hallucinated workbench target error = %v", err)
	}
}

func TestCommandCannotExecuteWithoutAcceptedProposal(t *testing.T) {
	command := conversationCommandFixture()
	store := &conversationStoreStub{command: command, proposalAccepted: false}
	runtimeStub := &conversationRuntimeStub{}
	service, err := newService(store, &conversationAccessStub{}, runtimeStub, conversationManifestStub{}, conversationProviderStub{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ExecuteCommand(context.Background(), command.ProjectID, command.ConversationID, command.ID, uuid.NewString(), command.ETag, ExecuteCommandInput{})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("unapproved command error = %v", err)
	}
	if runtimeStub.starts.Load() != 0 {
		t.Fatal("unapproved command reached workflow runtime")
	}
}

func TestConcurrentCommandExecutionStartsExactlyOneDeterministicRun(t *testing.T) {
	command := conversationCommandFixture()
	store := &conversationStoreStub{command: command, proposalAccepted: true}
	runtimeStub := &conversationRuntimeStub{}
	service, err := newService(store, &conversationAccessStub{}, runtimeStub, conversationManifestStub{}, conversationProviderStub{})
	if err != nil {
		t.Fatal(err)
	}
	actorID := uuid.NewString()
	const callers = 32
	var successes atomic.Int64
	var conflicts atomic.Int64
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, executeErr := service.ExecuteCommand(context.Background(), command.ProjectID, command.ConversationID, command.ID, actorID, command.ETag, ExecuteCommandInput{})
			switch {
			case executeErr == nil && result.Status == CommandExecuted:
				successes.Add(1)
			case errors.Is(executeErr, core.ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected execution result=%+v err=%v", result, executeErr)
			}
		}()
	}
	close(start)
	wait.Wait()
	if successes.Load() != 1 || conflicts.Load() != callers-1 || runtimeStub.starts.Load() != 1 {
		t.Fatalf("successes=%d conflicts=%d starts=%d", successes.Load(), conflicts.Load(), runtimeStub.starts.Load())
	}
	runtimeStub.mu.Lock()
	defer runtimeStub.mu.Unlock()
	if runtimeStub.last.RunID != command.ID || runtimeStub.last.DefinitionVersionID != command.Payload.DefinitionVersionID || runtimeStub.last.InputManifest != command.Payload.ManifestIntent.InputManifest {
		t.Fatalf("workflow did not use deterministic approved identities: %+v", runtimeStub.last)
	}
}

func TestWorkbenchInstructionCompletesOnlyThroughExactResultProtocol(t *testing.T) {
	command := conversationCommandFixture()
	command.Kind = IntentWorkbenchInstruction
	runID, bundleID, implementationProposalID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	command.Payload.Workbench.ExpectedRunID = runID
	command.Payload.Workbench.ExpectedBundleID = bundleID
	store := &conversationStoreStub{command: command, proposalAccepted: true}
	runtimeStub := &conversationRuntimeStub{}
	service, err := newService(store, &conversationAccessStub{}, runtimeStub, conversationManifestStub{}, conversationProviderStub{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ExecuteCommand(context.Background(), command.ProjectID, command.ConversationID, command.ID, uuid.NewString(), command.ETag, ExecuteCommandInput{
		WorkbenchResult: &WorkbenchExecutionResult{RunID: runID, BundleID: bundleID, ImplementationProposalID: implementationProposalID},
	})
	if err != nil || result.Status != CommandExecuted || !bytes.Contains(result.Result, []byte(runID)) ||
		!bytes.Contains(result.Result, []byte(bundleID)) || !bytes.Contains(result.Result, []byte(implementationProposalID)) {
		t.Fatalf("workbench result was not linked: result=%+v err=%v", result, err)
	}
	if runtimeStub.starts.Load() != 0 {
		t.Fatal("workbench callback incorrectly started a new workflow")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.atomicWorkbenchCalls != 1 || store.claimCalls != 0 {
		t.Fatalf("workbench completion was not atomic: atomic=%d ordinaryClaims=%d", store.atomicWorkbenchCalls, store.claimCalls)
	}
}

func TestNormalizeProposalRequiresExactImmutableInputs(t *testing.T) {
	valid := proposalInputFixture()
	if _, err := normalizeProposalInput(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.SourceRefs = append(invalid.SourceRefs, invalid.SourceRefs[0])
	if _, err := normalizeProposalInput(invalid); err == nil {
		t.Fatal("duplicate exact source reference was accepted")
	}
	invalid = valid
	invalid.ManifestIntent.Mode = "latest"
	if _, err := normalizeProposalInput(invalid); err == nil {
		t.Fatal("mutable latest-manifest intent was accepted")
	}
	invalid = valid
	invalid.Scope = json.RawMessage(`[]`)
	if _, err := normalizeProposalInput(invalid); err == nil {
		t.Fatal("non-object workflow scope was accepted")
	}
	invalid = valid
	invalid.Scope = json.RawMessage(`{"conversationIntent":{"proposalId":"client-selected"}}`)
	if _, err := normalizeProposalInput(invalid); err == nil {
		t.Fatal("client-supplied conversationIntent identities were accepted")
	}
	invalid = valid
	invalid.Kind = IntentWorkbenchInstruction
	if _, err := normalizeProposalInput(invalid); err == nil {
		t.Fatal("workbench instruction without an exact existing run was accepted")
	}
}

func TestReviewedConversationIntentScopeUsesOnlyServerMintedExactIdentities(t *testing.T) {
	input := proposalInputFixture()
	normalized, err := normalizeProposalInput(input)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, triggerID := uuid.New(), uuid.MustParse(input.TriggerMessageID)
	proposalID, assistantID := uuid.New(), uuid.New()
	scope, err := reviewedConversationIntentScope(normalized.Scope, conversationID, triggerID, proposalID, assistantID, normalized)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := platformdomain.CanonicalJSON(json.RawMessage(scope))
	if err != nil || !bytes.Equal(scope, canonical) {
		t.Fatalf("reviewed scope is not canonical: %s err=%v", scope, err)
	}
	var envelope struct {
		Slice              string                     `json:"slice"`
		ConversationIntent ReviewedConversationIntent `json:"conversationIntent"`
	}
	if err := json.Unmarshal(scope, &envelope); err != nil {
		t.Fatal(err)
	}
	intent := envelope.ConversationIntent
	if envelope.Slice != "all" || intent.ConversationID != conversationID.String() || intent.TriggerMessageID != triggerID.String() ||
		intent.ProposalID != proposalID.String() || intent.AssistantMessageID != assistantID.String() || intent.Kind != input.Kind ||
		intent.DefinitionVersionID != input.SuggestedDefinitionVersionID || intent.ManifestIntent.InputManifest != input.ManifestIntent.InputManifest ||
		len(intent.SourceRefs) != 1 || !intent.SourceRefs[0].Equal(input.SourceRefs[0]) || intent.WorkbenchInstruction.Objective != input.WorkbenchInstruction.Objective {
		t.Fatalf("reviewed scope lost exact server/source identities: %+v", intent)
	}

	sources, _ := platformdomain.CanonicalJSON(input.SourceRefs)
	manifest, _ := platformdomain.CanonicalJSON(input.ManifestIntent)
	instruction, _ := platformdomain.CanonicalJSON(input.WorkbenchInstruction)
	payload, err := commandPayloadFromProposal(storage.WorkflowIntentProposalModel{
		SuggestedDefinitionVersionID: uuid.MustParse(input.SuggestedDefinitionVersionID),
		Scope:                        scope, SourceRefs: sources, ManifestIntent: manifest, WorkbenchInstruction: instruction,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload.Scope, scope) {
		t.Fatalf("accepted command did not retain the exact reviewed canonical scope: proposal=%s command=%s", scope, payload.Scope)
	}
}

func TestCommandPayloadUsesStableWorkbenchWireKey(t *testing.T) {
	encoded, err := json.Marshal(conversationCommandFixture().Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"workbench"`)) || bytes.Contains(encoded, []byte(`"workbenchInstruction"`)) {
		t.Fatalf("unexpected command payload wire shape: %s", encoded)
	}
}

func proposalInputFixture() CreateIntentProposalInput {
	hash, _ := platformdomain.CanonicalHash(map[string]any{"immutable": true})
	return CreateIntentProposalInput{
		TriggerMessageID: uuid.NewString(), AssistantContent: "I propose the approved minimum loop.",
		Kind: IntentStartWorkflow, SuggestedDefinitionVersionID: uuid.NewString(), Scope: json.RawMessage(`{"slice":"all"}`),
		SourceRefs:           []platformdomain.ArtifactRef{{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: hash, AnchorID: "goal-1"}},
		ManifestIntent:       ManifestIntent{Mode: "use_existing", InputManifest: platformdomain.ManifestRef{ID: uuid.NewString(), Hash: hash}, Purpose: "requirements to application"},
		WorkbenchInstruction: WorkbenchInstruction{Objective: "Produce the reviewed application", Constraints: []string{"Preserve exact traceability"}},
	}
}

func proposalInputWithManifest(t *testing.T, projectID, actorID string) (CreateIntentProposalInput, platformdomain.InputManifest) {
	t.Helper()
	input := proposalInputFixture()
	manifest, err := platformdomain.NewInputManifest(
		input.ManifestIntent.InputManifest.ID, projectID, "conversation.workflow_intent", "", nil,
		[]platformdomain.ManifestSource{{Ref: input.SourceRefs[0], Purpose: "requirements to application"}},
		json.RawMessage(`{}`), "1", actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	input.ManifestIntent.InputManifest = manifest.Ref()
	return input, manifest
}

func conversationCommandFixture() ConversationCommand {
	input := proposalInputFixture()
	id := uuid.NewString()
	return ConversationCommand{
		ID: id, ProjectID: uuid.NewString(), ConversationID: uuid.NewString(), ProposalID: uuid.NewString(),
		Kind: IntentStartWorkflow, Status: CommandPending, Version: 1, ETag: CommandETag(id, 1),
		Payload: CommandPayload{
			DefinitionVersionID: input.SuggestedDefinitionVersionID, Scope: input.Scope, SourceRefs: input.SourceRefs,
			ManifestIntent: input.ManifestIntent, Workbench: input.WorkbenchInstruction,
		},
		AcceptedBy: uuid.NewString(),
	}
}

func cloneCommand(value ConversationCommand) ConversationCommand {
	value.Payload.Scope = append(json.RawMessage(nil), value.Payload.Scope...)
	value.Payload.SourceRefs = append([]platformdomain.ArtifactRef(nil), value.Payload.SourceRefs...)
	value.Result = append(json.RawMessage(nil), value.Result...)
	return value
}
