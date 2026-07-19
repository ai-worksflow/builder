package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func testApplicationBuildContractRef() core.ApplicationBuildContractRef {
	return core.ApplicationBuildContractRef{
		ID: "66666666-6666-4666-8666-666666666666", ContractHash: strings.Repeat("a", 64),
	}
}

type implementationBuildContractVerifierStub struct {
	calls           int
	projectID       string
	buildManifestID string
	actorID         string
	selection       core.ApplicationBuildContractRef
	verified        core.ApplicationBuildContractRef
	err             error
}

func (v *implementationBuildContractVerifierStub) RequireReadyForImplementation(
	_ context.Context,
	projectID, buildManifestID, actorID string,
	selection core.ApplicationBuildContractRef,
) (core.ApplicationBuildContractRef, error) {
	v.calls++
	v.projectID, v.buildManifestID, v.actorID, v.selection = projectID, buildManifestID, actorID, selection
	if v.err != nil {
		return core.ApplicationBuildContractRef{}, v.err
	}
	if v.verified == (core.ApplicationBuildContractRef{}) {
		return selection, nil
	}
	return v.verified, nil
}

type scriptedArtifactProvider struct {
	requests []ai.Request
	results  []ai.Result
	failures []error
}

func (p *scriptedArtifactProvider) Generate(_ context.Context, request ai.Request) (ai.Result, error) {
	index := len(p.requests)
	p.requests = append(p.requests, request)
	if index < len(p.failures) && p.failures[index] != nil {
		return ai.Result{}, p.failures[index]
	}
	if index >= len(p.results) {
		return ai.Result{}, errors.New("unexpected provider call")
	}
	return p.results[index], nil
}

func generatedArtifactResult(t *testing.T, candidate json.RawMessage, usage ai.Usage) ai.Result {
	t.Helper()
	payload, err := json.Marshal(artifactProposalAIOutput{
		Operations: []artifactProposalAIOperation{{
			ID: "replace-root", Kind: domain.OperationReplace, Path: "", ValueJSON: string(candidate),
			DependsOn: []string{}, Rationale: "Generate the canonical artifact.",
		}},
		Assumptions: []string{}, Questions: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ai.Result{Provider: "test", Model: "test-model", Output: payload, Usage: &usage}
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

func TestImplementationGenerationRequiresGovernedCandidateBeforeWorkbenchOrAI(t *testing.T) {
	t.Parallel()

	workbench := &blockedImplementationWorkbench{}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider}
	_, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "bundle", ActorID: "actor", Model: "gpt-5",
		Instruction: ImplementationInstruction{Objective: "build the application"},
	})
	if !errors.Is(err, ErrGovernedCandidateRequired) {
		t.Fatalf("legacy implementation generation error = %v, want governed Candidate", err)
	}
	if workbench.calls != 0 || provider.calls != 0 {
		t.Fatalf("legacy generation reached Workbench or AI: workbench=%d provider=%d", workbench.calls, provider.calls)
	}
}

func TestImplementationGenerationRejectsCompletedManifestBeforeAI(t *testing.T) {
	t.Parallel()

	workbench := &blockedImplementationWorkbench{}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider, allowUngovernedImplementationForTests: true}
	if _, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "consumed-bundle", ActorID: "actor", Model: "model",
		Instruction:              ImplementationInstruction{Objective: "instruction"},
		ApplicationBuildContract: testApplicationBuildContractRef(),
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
	service := &Service{workbench: workbench, provider: provider, allowUngovernedImplementationForTests: true}
	if _, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "old-workspace-bundle", ActorID: "actor", Model: "model",
		Instruction:              ImplementationInstruction{Objective: "instruction"},
		ApplicationBuildContract: testApplicationBuildContractRef(),
	}); !errors.Is(err, core.ErrProposalStale) {
		t.Fatalf("expected stale workspace manifest to block generation, got %v", err)
	}
	if workbench.calls != 1 || provider.calls != 0 {
		t.Fatalf("AI ran before exact workspace gate: workbench=%d provider=%d", workbench.calls, provider.calls)
	}
}

func TestImplementationGenerationRequiresExactBuildContractBeforeWorkbench(t *testing.T) {
	t.Parallel()

	workbench := &blockedImplementationWorkbench{}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider, allowUngovernedImplementationForTests: true}
	_, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "bundle", ActorID: "actor", Model: "gpt-5",
		Instruction: ImplementationInstruction{Objective: "build"},
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("missing exact Build Contract error = %v, want invalid input", err)
	}
	if workbench.calls != 0 || provider.calls != 0 {
		t.Fatalf("missing Build Contract reached Workbench or AI: workbench=%d provider=%d", workbench.calls, provider.calls)
	}
}

func TestImplementationGenerationVerifiesBuildContractBeforeRecovery(t *testing.T) {
	t.Parallel()

	ref := testApplicationBuildContractRef()
	gate := &implementationBuildContractVerifierStub{err: core.ErrBlockingGate}
	workbench := &recoveryPermissionWorkbench{
		bundle: core.WorkbenchBundle{ID: "active-bundle", ProjectID: "33333333-3333-4333-8333-333333333333"},
		proposal: core.ImplementationProposal{
			ID: "proposal-id", CreatedBy: "editor", Status: "open", Version: 1,
			ExecutionSource:          core.ImplementationSourceManualGeneration,
			ApplicationBuildContract: &ref,
		},
	}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider, buildContracts: gate, allowUngovernedImplementationForTests: true}
	_, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "root-bundle", ActorID: "editor", Model: "gpt-5", ProposalID: "proposal-id",
		Instruction:              ImplementationInstruction{Objective: "recover"},
		ApplicationBuildContract: ref,
	})
	if !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("blocked Build Contract recovery error = %v, want blocking gate", err)
	}
	if gate.calls != 1 || gate.buildManifestID != workbench.bundle.ID || provider.calls != 0 {
		t.Fatalf("recovery bypassed exact Build Contract gate: gate=%#v provider=%d", gate, provider.calls)
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
	service := &Service{workbench: workbench, provider: provider, allowUngovernedImplementationForTests: true}
	if _, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "root-bundle", ActorID: "viewer", Model: "model", ProposalID: "proposal-id",
		Instruction:              ImplementationInstruction{Objective: "recover proposal"},
		ApplicationBuildContract: testApplicationBuildContractRef(),
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
	service := &Service{
		workbench: workbench, provider: provider,
		buildContracts:                        &implementationBuildContractVerifierStub{},
		allowUngovernedImplementationForTests: true,
	}
	if _, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
		BundleID: "root-bundle", ActorID: "second-actor", Model: "gpt-5",
		Instruction:     ImplementationInstruction{Objective: "second reviewed command"},
		ExecutionSource: core.ImplementationSourceConversationCommand,
		RequestKey:      secondCommandID, ProposalID: secondCommandID, ConversationCommandID: &secondCommandID,
		ExpectedRunID:            "55555555-5555-4555-8555-555555555555",
		ExpectedRootBundleID:     "44444444-4444-4444-8444-444444444444",
		ApplicationBuildContract: testApplicationBuildContractRef(),
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
			service := &Service{workbench: workbench, provider: provider, allowUngovernedImplementationForTests: true}
			_, err := service.GenerateImplementation(context.Background(), ImplementationGenerationRequest{
				BundleID: "root-bundle", ActorID: "editor", Model: "gpt-5",
				Instruction:              ImplementationInstruction{Objective: "reviewed command"},
				ExecutionSource:          core.ImplementationSourceConversationCommand,
				RequestKey:               commandID,
				ProposalID:               commandID,
				ConversationCommandID:    &commandID,
				ExpectedRunID:            target.runID,
				ExpectedRootBundleID:     target.rootID,
				ApplicationBuildContract: testApplicationBuildContractRef(),
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
	buildContract := testApplicationBuildContractRef()
	requested, err := currentImplementationGenerationReplayIdentity(instruction, instructionHash, "gpt-5", buildContract)
	if err != nil {
		t.Fatal(err)
	}
	existing := storage.ImplementationGenerationClaimModel{
		Instruction: json.RawMessage(fmt.Sprintf(
			`{"instruction":%s,"applicationBuildContract":{"contractHash":%q,"id":%q}}`,
			instruction, buildContract.ContractHash, buildContract.ID,
		)),
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
	existing.Instruction = json.RawMessage(fmt.Sprintf(
		`{"applicationBuildContract":{"id":%q,"contractHash":%q},"instruction":{"constraints":["changed"],"objective":"Build the app"}}`,
		buildContract.ID, buildContract.ContractHash,
	))
	if implementationGenerationReplayMatches(existing, requested) {
		t.Fatal("retry changed canonical instruction without conflict")
	}
	existing.Instruction = append(json.RawMessage(nil), requested.Instruction...)
	changedBuildContract := buildContract
	changedBuildContract.ContractHash = strings.Repeat("b", 64)
	changedReplay, err := currentImplementationGenerationReplayIdentity(instruction, instructionHash, "gpt-5", changedBuildContract)
	if err != nil {
		t.Fatal(err)
	}
	existing.Instruction = changedReplay.Instruction
	if implementationGenerationReplayMatches(existing, requested) {
		t.Fatal("retry changed the exact Application Build Contract without conflict")
	}
	existing.Instruction = append(json.RawMessage(nil), requested.Instruction...)
	for name, mutate := range map[string]func(*storage.ImplementationGenerationClaimModel){
		"generation contract": func(value *storage.ImplementationGenerationClaimModel) {
			value.GenerationContractVersion = "implementation-proposal-generation/v3"
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
	buildContract := testApplicationBuildContractRef()
	input, err := marshalImplementationInput(bundle, "Build reviewed app", buildContract, nil, workflowInput, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := ai.Request{Input: input}
	var decoded struct {
		ApplicationBuildManifest core.WorkbenchBundle             `json:"applicationBuildManifest"`
		ApplicationBuildContract core.ApplicationBuildContractRef `json:"applicationBuildContract"`
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
	if decoded.ApplicationBuildContract != buildContract {
		t.Fatalf("AI request lost exact Application Build Contract identity: %#v", decoded.ApplicationBuildContract)
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

func TestArtifactProposalInstructionsIncludeCanonicalReviewContracts(t *testing.T) {
	t.Parallel()
	tests := map[string][]string{
		"refine_project_brief": {"top-level summary", "goal block", "blocking open question"},
		"derive_requirements":  {"top-level summary, blocks, requirements, and acceptanceCriteria", "sourceBlockIds", "acceptanceCriterionIds", "every Must requirement ID"},
		"decompose_pages": {
			"top-level nodes and edges", "never add a semantic alias", "non-empty title",
			"copied exactly from the frozen Requirement Baseline", "cover every Must requirement ID",
			"contains edge from a Feature", "requires edge to a Permission", "sourceNodeId",
		},
		"generate_page_spec": {
			"blueprintPageNodeId", "top-level acceptanceCriterionIds array", "every acceptance criterion linked to every requirementId",
			"ready, loading, empty, and error", "\"source\":\"api|database|fixture|local\"",
			"Keep dataBindings and interactions as empty arrays", "\"trigger\":\"explicit trigger\"",
		},
		"generate_prototype": {
			"pageSpecRevision", "state set exactly", "fixtureIds arrays that exactly match", "Never invent a fixture or interaction",
			"integer HTTP statusCode", "sanitized true", "declarative action", "semantic layer object record",
			"desktop, tablet, and mobile", "every required state and breakpoint pair", "Every named collection must be present",
			"\"layers\":{}", "\"tokenBindings\":[]", "viewportWidth", "fieldMetadata", "nonnegative integer x and y",
			"distinct, non-overlapping positions", "\"title\":\"State · Breakpoint\"",
		},
	}
	for jobType, required := range tests {
		jobType, required := jobType, required
		t.Run(jobType, func(t *testing.T) {
			t.Parallel()
			instructions := artifactProposalInstructions(jobType)
			for _, fragment := range required {
				if !strings.Contains(instructions, fragment) {
					t.Fatalf("instructions for %s do not contain %q: %s", jobType, fragment, instructions)
				}
			}
		})
	}
}

func TestDecomposePagesRepairsOneInvalidCandidateWithDeterministicFeedback(t *testing.T) {
	t.Parallel()
	base := json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[],"pageSpecs":[]}`)
	baseline := json.RawMessage(`{
  "requirements":[
    {"type":"requirement","requirementId":"REQ-A","priority":"must","acceptanceCriterionIds":["AC-A"]},
    {"type":"requirement","requirementId":"REQ-B","priority":"must","acceptanceCriterionIds":["AC-B"]},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-A"},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-B"}
  ]
}`)
	invalid := json.RawMessage(`{
  "schemaVersion":1,
  "nodes":[
    {"id":"feature-main","key":"FEATURE-MAIN","kind":"feature"},
    {"id":"page-main","key":"PAGE-MAIN","kind":"page","route":"/","userGoal":"Complete the primary task","requirementIds":["REQ-A","REQ-B"]}
  ],
  "edges":[{"id":"edge-main","sourceNodeId":"feature-main","targetNodeId":"page-main","kind":"contains"}]
}`)
	valid := json.RawMessage(`{
  "schemaVersion":1,
  "nodes":[
    {"id":"feature-main","key":"FEATURE-MAIN","kind":"feature","title":"Main"},
    {"id":"page-main","key":"PAGE-MAIN","kind":"page","title":"Main","route":"/","userGoal":"Complete the primary task","requirementIds":["REQ-A","REQ-B"]}
  ],
  "edges":[{"id":"edge-main","sourceNodeId":"feature-main","targetNodeId":"page-main","kind":"contains"}]
}`)
	provider := &scriptedArtifactProvider{results: []ai.Result{
		generatedArtifactResult(t, invalid, ai.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}),
		generatedArtifactResult(t, valid, ai.Usage{InputTokens: 40, OutputTokens: 50, TotalTokens: 90}),
	}}
	service := &Service{provider: provider}
	originalInput := json.RawMessage(`{"baseContent":{"schemaVersion":1,"nodes":[],"edges":[]},"sources":[]}`)
	result, _, err := service.generateValidatedArtifactOutput(
		context.Background(), "manifest", "decompose_pages", "test-model",
		originalInput, base, []json.RawMessage{baseline},
	)
	if err != nil {
		t.Fatalf("repair did not produce a canonical Blueprint: %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider calls = %d, want one generation plus one repair", len(provider.requests))
	}
	if result.Usage == nil || *result.Usage != (ai.Usage{InputTokens: 50, OutputTokens: 70, TotalTokens: 120}) {
		t.Fatalf("repair usage was not accumulated: %#v", result.Usage)
	}
	var repairInput map[string]any
	if err := json.Unmarshal(provider.requests[1].Input, &repairInput); err != nil {
		t.Fatal(err)
	}
	feedback, _ := repairInput["deterministicValidationFeedback"].(string)
	if repairInput["baseContent"] == nil || repairInput["previousInvalidProposal"] == nil ||
		!strings.Contains(feedback, "blueprint.page_title") || !strings.Contains(feedback, "non-empty title") {
		t.Fatalf("repair input lost immutable base, invalid candidate, or detailed feedback: %#v", repairInput)
	}
	if !strings.Contains(provider.requests[1].Instructions, "single deterministic repair pass") {
		t.Fatalf("repair request lacked its bounded repair contract: %s", provider.requests[1].Instructions)
	}
}

func TestDecomposePagesStopsAfterOneFailedRepair(t *testing.T) {
	t.Parallel()
	base := json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[]}`)
	invalid := json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[]}`)
	provider := &scriptedArtifactProvider{results: []ai.Result{
		generatedArtifactResult(t, invalid, ai.Usage{}),
		generatedArtifactResult(t, invalid, ai.Usage{}),
	}}
	service := &Service{provider: provider}
	_, _, err := service.generateValidatedArtifactOutput(
		context.Background(), "manifest", "decompose_pages", "test-model",
		json.RawMessage(`{"baseContent":{},"sources":[]}`), base, nil,
	)
	if !errors.Is(err, ai.ErrInvalidOutput) || len(provider.requests) != 2 {
		t.Fatalf("invalid repair was not bounded to two provider calls: calls=%d err=%v", len(provider.requests), err)
	}
}

func TestGeneratePageSpecRepairsInvalidCandidateWithDeterministicFeedback(t *testing.T) {
	t.Parallel()
	base := json.RawMessage(`{
  "schemaVersion":1,
  "blueprintPageNodeId":"page-a",
  "title":"Page A",
  "route":"/a",
  "userGoal":"Use Page A",
  "states":[],
  "dataBindings":[],
  "interactions":[]
}`)
	states := `[
  {"id":"state-ready","key":"ready","title":"Ready","required":true},
  {"id":"state-loading","key":"loading","title":"Loading","required":true},
  {"id":"state-empty","key":"empty","title":"Empty","required":true},
  {"id":"state-error","key":"error","title":"Error","required":true}
]`
	invalid := json.RawMessage(fmt.Sprintf(`{
  "schemaVersion":1,
  "blueprintPageNodeId":"page-a",
  "title":"Page A",
  "route":"/a",
  "userGoal":"Use Page A",
  "states":%s,
  "dataBindings":[],
  "interactions":[]
}`, states))
	valid := json.RawMessage(fmt.Sprintf(`{
  "schemaVersion":1,
  "blueprintPageNodeId":"page-a",
  "title":"Page A",
  "route":"/a",
  "userGoal":"Use Page A",
  "acceptanceCriterionIds":["AC-A"],
  "states":%s,
  "dataBindings":[],
  "interactions":[]
}`, states))
	provider := &scriptedArtifactProvider{results: []ai.Result{
		generatedArtifactResult(t, invalid, ai.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}),
		generatedArtifactResult(t, valid, ai.Usage{InputTokens: 40, OutputTokens: 50, TotalTokens: 90}),
	}}
	service := &Service{provider: provider}
	originalInput := json.RawMessage(`{"baseContent":{"blueprintPageNodeId":"page-a"},"sources":[]}`)
	result, _, err := service.generateValidatedArtifactOutput(
		context.Background(), "manifest", "generate_page_spec", "test-model",
		originalInput, base, nil,
	)
	if err != nil {
		t.Fatalf("repair did not produce a canonical PageSpec: %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider calls = %d, want one generation plus one repair", len(provider.requests))
	}
	if result.Usage == nil || *result.Usage != (ai.Usage{InputTokens: 50, OutputTokens: 70, TotalTokens: 120}) {
		t.Fatalf("repair usage was not accumulated: %#v", result.Usage)
	}
	var repairInput map[string]any
	if err := json.Unmarshal(provider.requests[1].Input, &repairInput); err != nil {
		t.Fatal(err)
	}
	feedback, _ := repairInput["deterministicValidationFeedback"].(string)
	if repairInput["baseContent"] == nil || repairInput["previousInvalidProposal"] == nil ||
		!strings.Contains(feedback, "page_spec.acceptance_trace") {
		t.Fatalf("repair input lost immutable base, invalid candidate, or detailed feedback: %#v", repairInput)
	}
	if !strings.Contains(provider.requests[1].Instructions, "single deterministic repair pass") ||
		!strings.Contains(provider.requests[1].Instructions, "top-level acceptanceCriterionIds array") {
		t.Fatalf("repair request lacked the PageSpec repair contract: %s", provider.requests[1].Instructions)
	}
}

func TestArtifactGenerationDoesNotRepairProviderFailures(t *testing.T) {
	t.Parallel()
	provider := &scriptedArtifactProvider{failures: []error{ai.ErrUnavailable}}
	service := &Service{provider: provider}
	_, _, err := service.generateValidatedArtifactOutput(
		context.Background(), "manifest", "decompose_pages", "test-model",
		json.RawMessage(`{"baseContent":{},"sources":[]}`),
		json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[]}`), nil,
	)
	if !errors.Is(err, ai.ErrUnavailable) || len(provider.requests) != 1 {
		t.Fatalf("provider failure triggered a repair: calls=%d err=%v", len(provider.requests), err)
	}
}

func TestBlueprintProposalPreflightUsesExactRequirementBaselineTrace(t *testing.T) {
	t.Parallel()
	base := json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[]}`)
	baseline := json.RawMessage(`{
  "requirements":[
    {"type":"requirement","requirementId":"REQ-A","priority":"must","acceptanceCriterionIds":["AC-A"]},
    {"type":"requirement","requirementId":"REQ-B","priority":"must","acceptanceCriterionIds":["AC-B"]},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-A"},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-B"}
  ]
}`)
	candidate := func(requirementIDs string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{
  "nodes":[
    {"id":"feature-main","key":"FEATURE-MAIN","kind":"feature"},
    {"id":"page-main","key":"PAGE-MAIN","kind":"page","title":"Main","route":"/","userGoal":"Complete the primary task","requirementIds":[%s]}
  ],
  "edges":[{"id":"edge-main","sourceNodeId":"feature-main","targetNodeId":"page-main","kind":"contains"}]
}`, requirementIDs))
	}
	preflight := func(content json.RawMessage) error {
		return preflightGeneratedArtifactProposal("decompose_pages", base, []domain.ProposalOperation{{
			ID: "replace-root", Kind: domain.OperationReplace, Path: "", Value: content,
		}}, baseline)
	}
	if err := preflight(candidate(`"REQ-A","REQ-B"`)); err != nil {
		t.Fatalf("exact complete Requirement Baseline trace was rejected: %v", err)
	}
	if err := preflight(candidate(`"REQ-A","REQ-UNKNOWN"`)); !errors.Is(err, ai.ErrInvalidOutput) || !strings.Contains(err.Error(), "REQ-UNKNOWN") {
		t.Fatalf("unknown Requirement Baseline ID passed preflight: %v", err)
	}
	if err := preflight(candidate(`"REQ-A"`)); !errors.Is(err, ai.ErrInvalidOutput) || !strings.Contains(err.Error(), "REQ-B") {
		t.Fatalf("missing Must Requirement Baseline coverage passed preflight: %v", err)
	}
}

func TestRequirementsProposalPreflightRejectsObservedDataOnlyAcceptanceLinks(t *testing.T) {
	t.Parallel()
	base := json.RawMessage(`{"schemaVersion":1,"kind":"productRequirements","blocks":[]}`)
	operations := make([]domain.ProposalOperation, 0, 8)
	for index := 1; index <= 4; index++ {
		requirementID := fmt.Sprintf("REQ-%03d", index)
		requirement, err := json.Marshal(map[string]any{
			"id": requirementID, "type": "requirement", "priority": "must", "text": "Required outcome",
		})
		if err != nil {
			t.Fatal(err)
		}
		criterion, err := json.Marshal(map[string]any{
			"id": fmt.Sprintf("AC-%03d", index), "type": "acceptanceCriterion", "text": "Observable result",
			"data": map[string]any{"relatedRequirementId": requirementID},
		})
		if err != nil {
			t.Fatal(err)
		}
		operations = append(operations,
			domain.ProposalOperation{ID: fmt.Sprintf("requirement-%d", index), Kind: domain.OperationAdd, Path: "/blocks/-", Value: requirement},
			domain.ProposalOperation{ID: fmt.Sprintf("criterion-%d", index), Kind: domain.OperationAdd, Path: "/blocks/-", Value: criterion},
		)
	}
	err := preflightGeneratedArtifactProposal("derive_requirements", base, operations)
	if !errors.Is(err, ai.ErrInvalidOutput) {
		t.Fatalf("preflight error = %v, want invalid AI output", err)
	}
	message := err.Error()
	if !strings.Contains(message, "requirements.summary_required@$.summary") ||
		strings.Count(message, "requirements.must_has_ac") != 4 {
		t.Fatalf("preflight did not expose the observed gate failures: %s", message)
	}
}

func TestRequirementsProposalPreflightAcceptsCanonicalBaselineInput(t *testing.T) {
	t.Parallel()
	base := json.RawMessage(`{"schemaVersion":1,"kind":"productRequirements","blocks":[]}`)
	content := json.RawMessage(`{
		"schemaVersion":1,
		"kind":"productRequirements",
		"summary":"Preserve exact immutable workflow lineage.",
		"blocks":[{"id":"source-brief","type":"paragraph","text":"Reviewed Project Brief context."}],
		"requirements":[{
			"id":"REQ-001","statement":"Preserve exact Proposal and Revision lineage.","priority":"must",
			"sourceBlockIds":["source-brief"],"acceptanceCriterionIds":["AC-001"]
		}],
		"acceptanceCriteria":[{"id":"AC-001","statement":"Every gate references the same immutable Revision hash."}]
	}`)
	operations := []domain.ProposalOperation{{
		ID: "replace-root", Kind: domain.OperationReplace, Path: "", Value: content,
	}}
	if err := preflightGeneratedArtifactProposal("derive_requirements", base, operations); err != nil {
		t.Fatalf("canonical requirements proposal failed preflight: %v", err)
	}
	if err := preflightGeneratedArtifactProposal("refine_project_brief", base, nil); err != nil {
		t.Fatalf("focused requirements preflight changed another job: %v", err)
	}
}

func TestBlueprintProposalPreflightRejectsPageFieldsOutsideCanonicalNodes(t *testing.T) {
	t.Parallel()
	base := json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[],"pageSpecs":[]}`)
	content := json.RawMessage(`{
		"schemaVersion":1,
		"nodes":[
			{"id":"feature-closure","businessKey":"feature-closure","type":"Feature"},
			{"id":"page-closure","businessKey":"page-closure","type":"Page","route":"/closure"}
		],
		"edges":[{"id":"contains-closure","from":"feature-closure","to":"page-closure","type":"contains"}],
		"pageSpecs":[{"nodeId":"page-closure","userGoal":"Verify closure","requirementIds":["REQ-001"]}]
	}`)
	err := preflightGeneratedArtifactProposal("decompose_pages", base, []domain.ProposalOperation{{
		ID: "replace-root", Kind: domain.OperationReplace, Path: "", Value: content,
	}})
	if !errors.Is(err, ai.ErrInvalidOutput) ||
		!strings.Contains(err.Error(), "blueprint.page_spec") ||
		!strings.Contains(err.Error(), "blueprint.page_requirement") {
		t.Fatalf("Blueprint preflight did not reject fields stored only outside Page nodes: %v", err)
	}

	valid := json.RawMessage(`{
		"schemaVersion":1,
		"nodes":[
			{"id":"feature-closure","businessKey":"feature-closure","type":"Feature","title":"Closure"},
			{"id":"page-closure","businessKey":"page-closure","type":"Page","title":"Closure","route":"/closure","userGoal":"Verify closure","requirementIds":["REQ-001"]}
		],
		"edges":[{"id":"contains-closure","from":"feature-closure","to":"page-closure","type":"contains"}],
		"pageSpecs":[]
	}`)
	if err := preflightGeneratedArtifactProposal("decompose_pages", base, []domain.ProposalOperation{{
		ID: "replace-root", Kind: domain.OperationReplace, Path: "", Value: valid,
	}}); err != nil {
		t.Fatalf("canonical Blueprint proposal failed preflight: %v", err)
	}
}

func TestDecodeArtifactProposalOutputValueJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		kind      domain.ProposalOperationKind
		valueJSON string
		want      string
		wantNil   bool
	}{
		{name: "object", kind: domain.OperationAdd, valueJSON: `{"b":2,"a":1}`, want: `{"a":1,"b":2}`},
		{name: "array", kind: domain.OperationReplace, valueJSON: `[1,{"b":2}]`, want: `[1,{"b":2}]`},
		{name: "string", kind: domain.OperationAdd, valueJSON: `"hello"`, want: `"hello"`},
		{name: "number", kind: domain.OperationReplace, valueJSON: "42.5", want: "42.5"},
		{name: "boolean", kind: domain.OperationAdd, valueJSON: "true", want: "true"},
		{name: "null", kind: domain.OperationReplace, valueJSON: "null", want: "null"},
		{name: "remove", kind: domain.OperationRemove, valueJSON: "null", wantNil: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(map[string]any{
				"operations": []any{map[string]any{
					"id": "op-1", "kind": test.kind, "path": "/title",
					"valueJson": test.valueJSON, "dependsOn": []string{}, "rationale": "test",
				}},
				"assumptions": []string{"assumption"},
				"questions":   []string{"question"},
			})
			if err != nil {
				t.Fatal(err)
			}
			output, err := decodeArtifactProposalOutput(payload)
			if err != nil {
				t.Fatal(err)
			}
			if len(output.Operations) != 1 {
				t.Fatalf("operation count = %d, want 1", len(output.Operations))
			}
			value := output.Operations[0].Value
			if test.wantNil {
				if value != nil {
					t.Fatalf("remove value = %s, want nil", value)
				}
			} else if string(value) != test.want {
				t.Fatalf("decoded value = %s, want %s", value, test.want)
			}
			if len(output.Assumptions) != 1 || output.Assumptions[0] != "assumption" ||
				len(output.Questions) != 1 || output.Questions[0] != "question" {
				t.Fatalf("proposal metadata was not preserved: %#v", output)
			}
		})
	}
}

func TestDecodeArtifactProposalOutputRejectsInvalidValueJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		kind      domain.ProposalOperationKind
		valueJSON string
	}{
		{name: "malformed add", kind: domain.OperationAdd, valueJSON: "{"},
		{name: "malformed replace", kind: domain.OperationReplace, valueJSON: "["},
		{name: "remove non-null", kind: domain.OperationRemove, valueJSON: `{"unexpected":true}`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(map[string]any{
				"operations": []any{map[string]any{
					"id": "op-1", "kind": test.kind, "path": "/title",
					"valueJson": test.valueJSON, "dependsOn": []string{}, "rationale": "test",
				}},
				"assumptions": []string{},
				"questions":   []string{},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeArtifactProposalOutput(payload); !errors.Is(err, ai.ErrInvalidOutput) {
				t.Fatalf("decode error = %v, want invalid AI output", err)
			}
		})
	}
}

func TestDecodeImplementationProposalOutputObjectJSON(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(map[string]any{
		"operations":         []any{},
		"routes":             []string{`{"method":"GET","path":"/"}`},
		"apis":               []string{`{"name":"health"}`},
		"migrations":         []string{`{"id":"001"}`},
		"tests":              []string{`{"name":"smoke"}`},
		"previews":           []string{`{"viewport":"desktop"}`},
		"traceLinks":         []string{`{"source":"REQ-1"}`},
		"diagnostics":        []any{},
		"assumptions":        []string{"assumption"},
		"unimplementedItems": []string{"later"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := decodeImplementationProposalOutput(payload)
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		got  []json.RawMessage
		want json.RawMessage
	}{
		"routes":     {got: output.Routes, want: json.RawMessage(`{"path":"/","method":"GET"}`)},
		"apis":       {got: output.APIs, want: json.RawMessage(`{"name":"health"}`)},
		"migrations": {got: output.Migrations, want: json.RawMessage(`{"id":"001"}`)},
		"tests":      {got: output.Tests, want: json.RawMessage(`{"name":"smoke"}`)},
		"previews":   {got: output.Previews, want: json.RawMessage(`{"viewport":"desktop"}`)},
		"traceLinks": {got: output.TraceLinks, want: json.RawMessage(`{"source":"REQ-1"}`)},
	} {
		if len(test.got) != 1 || !jsonBytesEqual(test.got[0], test.want) {
			t.Fatalf("%s = %s, want %s", name, test.got, test.want)
		}
	}
	if len(output.Assumptions) != 1 || output.Assumptions[0] != "assumption" ||
		len(output.UnimplementedItems) != 1 || output.UnimplementedItems[0] != "later" {
		t.Fatalf("implementation metadata was not preserved: %#v", output)
	}
}

func TestDecodeImplementationProposalOutputRejectsInvalidObjectJSON(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		field string
		value string
	}{
		"malformed":  {field: "routes", value: "{"},
		"non-object": {field: "apis", value: `[]`},
	} {
		t.Run(name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{test.field: []string{test.value}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeImplementationProposalOutput(payload); !errors.Is(err, ai.ErrInvalidOutput) {
				t.Fatalf("decode error = %v, want invalid AI output", err)
			}
		})
	}
}

func TestGenerationSchemasAreStrictStructuredOutputCompatible(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string]json.RawMessage{
		"artifact":       artifactProposalSchema,
		"implementation": implementationProposalSchema,
	} {
		t.Run(name, func(t *testing.T) {
			var schema any
			if err := json.Unmarshal(payload, &schema); err != nil {
				t.Fatal(err)
			}
			assertStrictGenerationSchema(t, name, schema)
		})
	}
}

func TestGenerationSchemasUseJSONTextAtOpenValueBoundaries(t *testing.T) {
	t.Parallel()
	var artifact map[string]any
	if err := json.Unmarshal(artifactProposalSchema, &artifact); err != nil {
		t.Fatal(err)
	}
	operationProperties := generationSchemaMapAt(t, artifact, "properties", "operations", "items", "properties")
	if _, exists := operationProperties["value"]; exists {
		t.Fatal("artifact AI schema must not expose an unconstrained value field")
	}
	valueJSON := generationSchemaMapAt(t, operationProperties, "valueJson")
	if valueJSON["type"] != "string" {
		t.Fatalf("valueJson schema = %#v, want string", valueJSON)
	}

	var implementation map[string]any
	if err := json.Unmarshal(implementationProposalSchema, &implementation); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"routes", "apis", "migrations", "tests", "previews", "traceLinks"} {
		item := generationSchemaMapAt(t, implementation, "properties", field, "items")
		if item["type"] != "string" {
			t.Fatalf("%s item schema = %#v, want JSON text string", field, item)
		}
	}
}

func assertStrictGenerationSchema(t *testing.T, path string, node any) {
	t.Helper()
	switch value := node.(type) {
	case map[string]any:
		if len(value) == 0 {
			t.Fatalf("%s contains an empty schema", path)
		}
		if generationSchemaHasType(value["type"], "object") {
			additional, ok := value["additionalProperties"].(bool)
			if !ok || additional {
				t.Fatalf("%s object must set additionalProperties=false", path)
			}
			properties, ok := value["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s object must declare properties", path)
			}
			requiredValues, ok := value["required"].([]any)
			if !ok {
				t.Fatalf("%s object must require every property", path)
			}
			required := make(map[string]struct{}, len(requiredValues))
			for _, item := range requiredValues {
				if name, ok := item.(string); ok {
					required[name] = struct{}{}
				}
			}
			for property := range properties {
				if _, ok := required[property]; !ok {
					t.Fatalf("%s property %s is not required", path, property)
				}
			}
		}
		if generationSchemaHasType(value["type"], "array") {
			if _, ok := value["items"]; !ok {
				t.Fatalf("%s array must declare items", path)
			}
		}
		for key, child := range value {
			assertStrictGenerationSchema(t, path+"."+key, child)
		}
	case []any:
		for index, child := range value {
			assertStrictGenerationSchema(t, fmt.Sprintf("%s[%d]", path, index), child)
		}
	}
}

func generationSchemaHasType(value any, expected string) bool {
	switch typed := value.(type) {
	case string:
		return typed == expected
	case []any:
		for _, item := range typed {
			if item == expected {
				return true
			}
		}
	}
	return false
}

func generationSchemaMapAt(t *testing.T, root map[string]any, keys ...string) map[string]any {
	t.Helper()
	var current any = root
	path := "schema"
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object", path)
		}
		next, ok := object[key]
		if !ok {
			t.Fatalf("%s is missing %s", path, key)
		}
		current = next
		path += "." + key
	}
	result, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", path)
	}
	return result
}
