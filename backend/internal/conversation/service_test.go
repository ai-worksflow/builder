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
}

func (s *generatedConversationProviderStub) Generate(_ context.Context, request ai.Request) (ai.Result, error) {
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
	mu               sync.Mutex
	appendCalls      int
	proposalAccepted bool
	command          ConversationCommand
	claimToken       *uuid.UUID
	proposal         WorkflowIntentProposal
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
func (s *conversationStoreStub) CreateIntentProposal(_ context.Context, _, conversationID, actorID uuid.UUID, input CreateIntentProposalInput, provenance ProposalProvenance) (WorkflowIntentProposal, Message, error) {
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
func (s *conversationStoreStub) IntentGenerationContext(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []uuid.UUID, []platformdomain.ArtifactRef, ManifestIntent) (intentGenerationContext, error) {
	return intentGenerationContext{}, nil
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
	runID, bundleID := uuid.NewString(), uuid.NewString()
	command.Payload.Workbench.ExpectedRunID = runID
	store := &conversationStoreStub{command: command, proposalAccepted: true}
	runtimeStub := &conversationRuntimeStub{}
	service, err := newService(store, &conversationAccessStub{}, runtimeStub, conversationManifestStub{}, conversationProviderStub{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ExecuteCommand(context.Background(), command.ProjectID, command.ConversationID, command.ID, uuid.NewString(), command.ETag, ExecuteCommandInput{
		WorkbenchResult: &WorkbenchExecutionResult{RunID: runID, BundleID: bundleID},
	})
	if err != nil || result.Status != CommandExecuted || !bytes.Contains(result.Result, []byte(runID)) || !bytes.Contains(result.Result, []byte(bundleID)) {
		t.Fatalf("workbench result was not linked: result=%+v err=%v", result, err)
	}
	if runtimeStub.starts.Load() != 0 {
		t.Fatal("workbench callback incorrectly started a new workflow")
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
	invalid.Kind = IntentWorkbenchInstruction
	if _, err := normalizeProposalInput(invalid); err == nil {
		t.Fatal("workbench instruction without an exact existing run was accepted")
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
		input.ManifestIntent.InputManifest.ID, projectID, "conversation_intent", "", nil,
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
