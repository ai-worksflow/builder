package generation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

type blockedImplementationWorkbench struct{ calls int }

func (w *blockedImplementationWorkbench) GetBundleForGeneration(context.Context, string, string) (core.WorkbenchBundle, error) {
	w.calls++
	return core.WorkbenchBundle{}, core.ErrBlockingGate
}
func (w *blockedImplementationWorkbench) GetLineageState(context.Context, string, string) (core.WorkbenchLineageState, error) {
	w.calls++
	return core.WorkbenchLineageState{}, core.ErrBlockingGate
}

type staleImplementationWorkbench struct{ calls int }

func (w *staleImplementationWorkbench) GetBundleForGeneration(context.Context, string, string) (core.WorkbenchBundle, error) {
	w.calls++
	return core.WorkbenchBundle{}, core.ErrProposalStale
}
func (w *staleImplementationWorkbench) GetLineageState(context.Context, string, string) (core.WorkbenchLineageState, error) {
	w.calls++
	return core.WorkbenchLineageState{}, core.ErrProposalStale
}

type generationProviderSpy struct{ calls int }

func (p *generationProviderSpy) Generate(context.Context, ai.Request) (ai.Result, error) {
	p.calls++
	return ai.Result{}, nil
}

type recoveryPermissionWorkbench struct {
	proposal    core.ImplementationProposal
	bundleCalls int
	bundle      core.WorkbenchBundle
	bundleErr   error
}

func (w *recoveryPermissionWorkbench) GetLineageState(context.Context, string, string) (core.WorkbenchLineageState, error) {
	runID := "55555555-5555-4555-8555-555555555555"
	return core.WorkbenchLineageState{
		RootBundleID:    "44444444-4444-4444-8444-444444444444",
		ActiveBundle:    core.WorkbenchBundle{ID: "active-bundle", WorkflowRunID: &runID},
		CurrentProposal: &w.proposal,
	}, nil
}

func (w *recoveryPermissionWorkbench) GetBundleForGeneration(context.Context, string, string) (core.WorkbenchBundle, error) {
	w.bundleCalls++
	return w.bundle, w.bundleErr
}

func TestImplementationGenerationRejectsCompletedManifestBeforeAI(t *testing.T) {
	t.Parallel()

	workbench := &blockedImplementationWorkbench{}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider}
	if _, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "consumed-bundle", ActorID: "actor", Model: "model",
		Instruction: ImplementationInstruction{Objective: "instruction"},
	}); !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("expected completed manifest to block generation, got %v", err)
	}
	if workbench.calls != 1 || provider.calls != 0 {
		t.Fatalf("AI ran before manifest readiness gate: workbench=%d provider=%d", workbench.calls, provider.calls)
	}
}

func TestImplementationGenerationRequiresCurrentWorkspaceBeforeAI(t *testing.T) {
	t.Parallel()

	workbench := &staleImplementationWorkbench{}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider}
	if _, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "old-workspace-bundle", ActorID: "actor", Model: "model",
		Instruction: ImplementationInstruction{Objective: "instruction"},
	}); !errors.Is(err, core.ErrProposalStale) {
		t.Fatalf("expected stale workspace manifest to block generation, got %v", err)
	}
	if workbench.calls != 1 || provider.calls != 0 {
		t.Fatalf("AI ran before exact workspace gate: workbench=%d provider=%d", workbench.calls, provider.calls)
	}
}

func TestImplementationRecoveryRequiresEditPermissionBeforeReturningProposal(t *testing.T) {
	t.Parallel()

	_, _, instructionHash, err := CanonicalImplementationInstruction("recover proposal", nil)
	if err != nil {
		t.Fatal(err)
	}
	workbench := &recoveryPermissionWorkbench{bundleErr: core.ErrForbidden, proposal: core.ImplementationProposal{
		ID: "proposal-id", CreatedBy: "viewer", Status: "open", Version: 1,
		ExecutionSource: core.ImplementationSourceManualGeneration,
		InstructionHash: instructionHash, AIProvider: "provider", AIModel: "model",
	}}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider}
	if _, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "root-bundle", ActorID: "viewer", Model: "model", ProposalID: "proposal-id",
		Instruction: ImplementationInstruction{Objective: "recover proposal"},
	}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("expected recovery to require edit permission, got %v", err)
	}
	if workbench.bundleCalls != 1 || provider.calls != 0 {
		t.Fatalf("recovery bypassed edit authorization: bundle=%d provider=%d", workbench.bundleCalls, provider.calls)
	}
}

func TestSecondConversationCommandCannotReplaceFirstCommandProposal(t *testing.T) {
	t.Parallel()

	firstCommandID := "11111111-1111-4111-8111-111111111111"
	secondCommandID := "22222222-2222-4222-8222-222222222222"
	firstCommandIDPointer := firstCommandID
	workbench := &recoveryPermissionWorkbench{
		bundle: core.WorkbenchBundle{ID: "active-bundle", ProjectID: "33333333-3333-4333-8333-333333333333"},
		proposal: core.ImplementationProposal{
			ID: firstCommandID, CreatedBy: "first-actor", Status: "open", Version: 1,
			ExecutionSource:       core.ImplementationSourceConversationCommand,
			ConversationCommandID: &firstCommandIDPointer,
		},
	}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider}
	if _, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "root-bundle", ActorID: "second-actor", Model: "gpt-5",
		Instruction:     ImplementationInstruction{Objective: "second reviewed command"},
		ExecutionSource: core.ImplementationSourceConversationCommand,
		RequestKey:      secondCommandID, ProposalID: secondCommandID, ConversationCommandID: &secondCommandID,
		ExpectedRunID:        "55555555-5555-4555-8555-555555555555",
		ExpectedRootBundleID: "44444444-4444-4444-8444-444444444444",
	}); !errors.Is(err, ErrActiveImplementationProposal) {
		t.Fatalf("second command replaced or bypassed the first command proposal: %v", err)
	}
	if provider.calls != 0 || workbench.bundleCalls != 1 {
		t.Fatalf("AI ran while a conversation-owned proposal was active: provider=%d bundle=%d", provider.calls, workbench.bundleCalls)
	}
}

func TestConversationImplementationGenerationRequiresExpectedTargetBeforeWorkbench(t *testing.T) {
	t.Parallel()

	commandID := "22222222-2222-4222-8222-222222222222"
	for name, target := range map[string]struct {
		runID  string
		rootID string
	}{
		"missing run":  {rootID: "44444444-4444-4444-8444-444444444444"},
		"missing root": {runID: "55555555-5555-4555-8555-555555555555"},
		"invalid run": {
			runID: "not-a-run", rootID: "44444444-4444-4444-8444-444444444444",
		},
		"invalid root": {
			runID: "55555555-5555-4555-8555-555555555555", rootID: "not-a-root",
		},
	} {
		t.Run(name, func(t *testing.T) {
			workbench := &blockedImplementationWorkbench{}
			provider := &generationProviderSpy{}
			service := &Service{workbench: workbench, provider: provider}
			_, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
				BundleID: "root-bundle", ActorID: "editor", Model: "gpt-5",
				Instruction:           ImplementationInstruction{Objective: "reviewed command"},
				ExecutionSource:       core.ImplementationSourceConversationCommand,
				RequestKey:            commandID,
				ProposalID:            commandID,
				ConversationCommandID: &commandID,
				ExpectedRunID:         target.runID,
				ExpectedRootBundleID:  target.rootID,
			})
			if !errors.Is(err, core.ErrInvalidInput) {
				t.Fatalf("generation error = %v, want invalid input", err)
			}
			if workbench.calls != 0 || provider.calls != 0 {
				t.Fatalf("invalid target reached workbench or AI: workbench=%d provider=%d", workbench.calls, provider.calls)
			}
		})
	}
}

func TestConversationOwnedProposalCannotBeSuperseded(t *testing.T) {
	t.Parallel()
	if supersedableImplementationGenerationProposal(core.ImplementationProposal{
		Status: "open", Version: 1, ExecutionSource: core.ImplementationSourceConversationCommand,
	}) {
		t.Fatal("a later command or manual generation must not supersede a conversation-owned proposal")
	}
}

func TestImplementationGenerationReplayIdentityIsExactAndCanonical(t *testing.T) {
	t.Parallel()

	_, instruction, instructionHash, err := CanonicalImplementationInstruction("  Build the app  ", []string{"  keep tests  "})
	if err != nil {
		t.Fatal(err)
	}
	requested := currentImplementationGenerationReplayIdentity(instruction, instructionHash, "gpt-5")
	existing := storage.ImplementationGenerationClaimModel{
		Instruction:     json.RawMessage(`{ "objective": "Build the app", "constraints": ["keep tests"] }`),
		InstructionHash: instructionHash, RequestedModel: "gpt-5",
		GenerationContractVersion: requested.GenerationContractVersion,
		SystemPromptHash:          requested.SystemPromptHash, OutputSchemaHash: requested.OutputSchemaHash,
	}
	if !implementationGenerationReplayMatches(existing, requested) {
		t.Fatal("semantically identical JSONB replay identity did not match")
	}
	existing.RequestedModel = "different-model"
	if implementationGenerationReplayMatches(existing, requested) {
		t.Fatal("retry changed requested model without conflict")
	}
	existing.RequestedModel = "gpt-5"
	existing.Instruction = json.RawMessage(`{"constraints":["changed"],"objective":"Build the app"}`)
	if implementationGenerationReplayMatches(existing, requested) {
		t.Fatal("retry changed canonical instruction without conflict")
	}
	existing.Instruction = instruction
	for name, mutate := range map[string]func(*storage.ImplementationGenerationClaimModel){
		"generation contract": func(value *storage.ImplementationGenerationClaimModel) {
			value.GenerationContractVersion = "implementation-proposal-generation/v2"
		},
		"system prompt": func(value *storage.ImplementationGenerationClaimModel) {
			value.SystemPromptHash = generationSHA256([]byte("changed prompt"))
		},
		"output schema": func(value *storage.ImplementationGenerationClaimModel) {
			value.OutputSchemaHash = generationSHA256([]byte("changed schema"))
		},
	} {
		candidate := existing
		mutate(&candidate)
		if implementationGenerationReplayMatches(candidate, requested) {
			t.Fatalf("retry changed %s without conflict", name)
		}
	}
	if !validGenerationSHA256(requested.SystemPromptHash) || !validGenerationSHA256(requested.OutputSchemaHash) {
		t.Fatalf("generation contract hashes are invalid: %#v", requested)
	}
}

func TestImplementationGenerationGovernanceRecoveryRequiresExactManifestAndSources(t *testing.T) {
	t.Parallel()
	manifestID := uuid.New()
	manifestHash := generationSHA256([]byte("governance-manifest"))
	refs := json.RawMessage(`[{"artifactId":"a","revisionId":"r","contentHash":"sha256:content"}]`)
	existing := storage.ImplementationGenerationClaimModel{
		GovernanceManifestID: &manifestID, GovernanceManifestHash: &manifestHash,
		GovernanceSourceRefs: json.RawMessage(`[ { "contentHash": "sha256:content", "revisionId": "r", "artifactId": "a" } ]`),
	}
	if !implementationGenerationGovernanceMatches(existing, &manifestID, manifestHash, refs) {
		t.Fatal("semantically identical governance JSONB did not match")
	}
	changedHash := generationSHA256([]byte("changed-governance"))
	if implementationGenerationGovernanceMatches(existing, &manifestID, changedHash, refs) {
		t.Fatal("recovery accepted a changed governance manifest hash")
	}
	changedRefs := json.RawMessage(`[{"artifactId":"a","revisionId":"other","contentHash":"sha256:content"}]`)
	if implementationGenerationGovernanceMatches(existing, &manifestID, manifestHash, changedRefs) {
		t.Fatal("recovery accepted changed governance source refs")
	}
}

func TestFailedConversationGenerationMayBeRetriedByAnotherEditor(t *testing.T) {
	t.Parallel()
	actorA, actorB := uuid.New(), uuid.New()
	if !implementationGenerationActorCompatible(core.ImplementationSourceConversationCommand, actorA, actorB) {
		t.Fatal("a reviewed command with no product could not be recovered by another authorized editor")
	}
	if implementationGenerationActorCompatible(core.ImplementationSourceManualGeneration, actorA, actorB) {
		t.Fatal("manual request identity unexpectedly allowed an actor takeover")
	}
}

func TestImplementationAIRequestPreservesWorkflowManifestPairsAndReviewedScope(t *testing.T) {
	t.Parallel()

	root := domain.ArtifactRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: generationSHA256([]byte("root")),
	}
	anchored := root
	anchored.AnchorID = "page.orders"
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), uuid.NewString(), "blueprint.selection", "selection-1", nil,
		[]domain.ManifestSource{
			{Ref: root, Purpose: "blueprint_selection_root"},
			{Ref: anchored, Purpose: "blueprint_selection_node"},
		},
		json.RawMessage(`{"blueprintSelection":{"selectionId":"selection-1","nodeIds":["page.orders"]}}`),
		"blueprint-selection/v1", uuid.NewString(), time.Unix(10, 0).UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runScope := json.RawMessage(`{"conversationIntent":{"kind":"start_workflow","proposalId":"reviewed-proposal","workbenchInstruction":{"objective":"Build reviewed app"}}}`)
	outputContract := &domain.WorkflowOutputContract{
		Capability: domain.WorkflowOutputApplication, ProducedArtifactKinds: []string{"workspace"},
		TerminalOutcome: domain.WorkflowOutcomeApplication, TerminalNodeType: domain.NodeWorkbenchBuild,
	}
	bundle := core.WorkbenchBundle{WorkflowContext: &core.ApplicationBuildContext{
		Definition:    domain.WorkflowDefinitionRef{ID: uuid.NewString(), Version: 3, Hash: generationSHA256([]byte("definition"))},
		InputManifest: manifest, DeliverySliceID: "slice-orders", RunScope: runScope, OutputContract: outputContract,
	}}
	workflowInput := map[string]any{
		"inputManifest": manifest,
		"sources": []any{
			map[string]any{"ref": root, "purpose": "blueprint_selection_root", "content": map[string]any{"title": "App"}},
			map[string]any{"ref": anchored, "purpose": "blueprint_selection_node", "content": map[string]any{"title": "Orders"}},
		},
	}
	input, err := marshalImplementationInput(bundle, "Build reviewed app", nil, workflowInput, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := ai.Request{Input: input}
	var decoded struct {
		ApplicationBuildManifest core.WorkbenchBundle `json:"applicationBuildManifest"`
		WorkflowInput            struct {
			InputManifest domain.InputManifest `json:"inputManifest"`
			Sources       []struct {
				Ref     domain.ArtifactRef `json:"ref"`
				Purpose string             `json:"purpose"`
				Content map[string]any     `json:"content"`
			} `json:"sources"`
		} `json:"workflowInput"`
	}
	if err := json.Unmarshal(request.Input, &decoded); err != nil {
		t.Fatal(err)
	}
	context := decoded.ApplicationBuildManifest.WorkflowContext
	if context == nil || len(context.InputManifest.Sources) != 2 ||
		!context.InputManifest.Sources[0].Ref.Equal(root) || context.InputManifest.Sources[0].Purpose != "blueprint_selection_root" ||
		!context.InputManifest.Sources[1].Ref.Equal(anchored) || context.InputManifest.Sources[1].Purpose != "blueprint_selection_node" ||
		!jsonBytesEqual(context.RunScope, runScope) || !jsonBytesEqual(context.InputManifest.Constraints, manifest.Constraints) {
		t.Fatalf("application build context lost exact selection/review evidence: %#v", context)
	}
	if len(decoded.WorkflowInput.Sources) != 2 || !decoded.WorkflowInput.Sources[1].Ref.Equal(anchored) ||
		decoded.WorkflowInput.Sources[1].Purpose != "blueprint_selection_node" || decoded.WorkflowInput.Sources[1].Content["title"] != "Orders" {
		t.Fatalf("AI request lost exact ref↔purpose↔content evidence: %#v", decoded.WorkflowInput.Sources)
	}
}

func TestRedactSensitiveStructuredValues(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"title":  "safe",
		"apiKey": "secret-value",
		"nested": map[string]any{"password": "hunter2", "note": "keep"},
	}
	redacted := redact(value, "").(map[string]any)
	if redacted["apiKey"] != "[REDACTED]" {
		t.Fatal("API key was not redacted")
	}
	nested := redacted["nested"].(map[string]any)
	if nested["password"] != "[REDACTED]" || nested["note"] != "keep" {
		t.Fatalf("unexpected nested redaction: %#v", nested)
	}
}

func TestRedactPreservesProductAndDesignTokenVocabulary(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"tokens":                      map[string]any{"color.primary": "#3366ff"},
		"tokenBindings":               []any{map[string]any{"node": "hero", "token": "color.primary"}},
		"cookiePolicy":                "SameSite=Lax",
		"secretManagementRequirement": "Use the platform vault",
		"accessToken":                 "credential",
		"client_secret":               "credential",
		"authorization":               "Bearer credential",
	}
	redacted := redact(value, "").(map[string]any)
	if redacted["tokens"].(map[string]any)["color.primary"] != "#3366ff" ||
		redacted["tokenBindings"].([]any)[0].(map[string]any)["token"] != "color.primary" ||
		redacted["cookiePolicy"] != "SameSite=Lax" || redacted["secretManagementRequirement"] != "Use the platform vault" {
		t.Fatalf("product/design vocabulary was over-redacted: %#v", redacted)
	}
	for _, key := range []string{"accessToken", "client_secret", "authorization"} {
		if redacted[key] != "[REDACTED]" {
			t.Fatalf("credential %s was not redacted: %#v", key, redacted[key])
		}
	}
}

func TestWorkspaceInputIncludesExpectedFileHash(t *testing.T) {
	t.Parallel()
	workspace := map[string]any{"files": []any{map[string]any{"path": "src/a.ts", "content": "hello"}}}
	result := workspaceWithFileHashes(workspace).(map[string]any)
	file := result["files"].([]any)[0].(map[string]any)
	if file["contentHash"] != "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("unexpected hash: %v", file["contentHash"])
	}
}

func TestGenerationSchemasAreValidJSON(t *testing.T) {
	t.Parallel()
	if !json.Valid(artifactProposalSchema) || !json.Valid(implementationProposalSchema) {
		t.Fatal("generation schemas must remain valid JSON")
	}
}
