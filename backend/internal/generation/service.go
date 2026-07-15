package generation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var sensitiveCredentialKeys = map[string]struct{}{
	"password": {}, "passwd": {}, "passphrase": {}, "clientsecret": {}, "apikey": {}, "privatekey": {},
	"authorization": {}, "authorizationheader": {}, "cookie": {}, "cookieheader": {}, "setcookie": {},
	"accesstoken": {}, "refreshtoken": {}, "idtoken": {}, "authtoken": {}, "bearertoken": {},
	"sessiontoken": {}, "csrftoken": {},
}

type ArtifactGenerationResult struct {
	Proposal coreProposal `json:"proposal"`
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	Usage    *ai.Usage    `json:"usage,omitempty"`
}

// coreProposal avoids hiding the domain contract while keeping this package's
// result stable if transport adds presentation fields.
type coreProposal = domain.OutputProposal

type artifactProposalAIOutput struct {
	Operations  []artifactProposalAIOperation `json:"operations"`
	Assumptions []string                      `json:"assumptions"`
	Questions   []string                      `json:"questions"`
}

type artifactProposalAIOperation struct {
	ID        string                       `json:"id"`
	Kind      domain.ProposalOperationKind `json:"kind"`
	Path      string                       `json:"path"`
	ValueJSON string                       `json:"valueJson"`
	DependsOn []string                     `json:"dependsOn"`
	Rationale string                       `json:"rationale"`
}

type artifactProposalOutput struct {
	Operations  []domain.ProposalOperation
	Assumptions []string
	Questions   []string
}

type implementationProposalAIOutput struct {
	Operations         []core.FileOperation     `json:"operations"`
	Routes             []string                 `json:"routes"`
	APIs               []string                 `json:"apis"`
	Migrations         []string                 `json:"migrations"`
	Tests              []string                 `json:"tests"`
	Previews           []string                 `json:"previews"`
	TraceLinks         []string                 `json:"traceLinks"`
	Diagnostics        []core.ValidationFinding `json:"diagnostics"`
	Assumptions        []string                 `json:"assumptions"`
	UnimplementedItems []string                 `json:"unimplementedItems"`
}

type ImplementationGenerationResult struct {
	Proposal core.ImplementationProposal `json:"proposal"`
	Provider string                      `json:"provider"`
	Model    string                      `json:"model"`
	Usage    *ai.Usage                   `json:"usage,omitempty"`
}

var (
	ErrImplementationGenerationProcessing = errors.New("implementation generation is already processing")
	ErrActiveImplementationProposal       = errors.New("active implementation proposal already exists")
)

type ImplementationInstruction struct {
	Objective   string   `json:"objective"`
	Constraints []string `json:"constraints"`
}

type ImplementationGenerationRequest struct {
	BundleID                      string
	ActorID                       string
	Model                         string
	Instruction                   ImplementationInstruction
	ExecutionSource               core.ImplementationExecutionSource
	RequestKey                    string
	ProposalID                    string
	ConversationCommandID         *string
	ExpectedRunID                 string
	ExpectedRootBundleID          string
	ExpectedActiveProposalID      string
	ExpectedActiveProposalVersion uint64
	GovernanceManifest            *domain.ManifestRef
	GovernanceSourceRefs          []domain.ArtifactRef
}

type ServiceConfig struct {
	ClaimLease time.Duration
}

type implementationWorkbench interface {
	GetBundleForGeneration(context.Context, string, string) (core.WorkbenchBundle, error)
	GetLineageState(context.Context, string, string) (core.WorkbenchLineageState, error)
}

type Service struct {
	database       *gorm.DB
	contents       content.Store
	provider       ai.Provider
	proposals      *core.ProposalService
	workbench      implementationWorkbench
	implementation *core.ImplementationService
	claimLease     time.Duration
	now            func() time.Time
}

func NewService(
	database *gorm.DB,
	contents content.Store,
	provider ai.Provider,
	proposals *core.ProposalService,
	workbench *core.WorkbenchService,
	implementation *core.ImplementationService,
	configs ...ServiceConfig,
) (*Service, error) {
	if database == nil || contents == nil || provider == nil || proposals == nil || workbench == nil || implementation == nil {
		return nil, errors.New("generation dependencies are required")
	}
	claimLease := 7 * time.Minute
	if len(configs) > 0 && configs[0].ClaimLease > 0 {
		claimLease = configs[0].ClaimLease
	}
	return &Service{
		database: database, contents: contents, provider: provider,
		proposals: proposals, workbench: workbench, implementation: implementation,
		claimLease: claimLease, now: time.Now,
	}, nil
}

func (s *Service) GenerateArtifactProposal(ctx context.Context, manifestID, actorID, model string) (ArtifactGenerationResult, error) {
	manifest, err := s.proposals.GetManifest(ctx, manifestID, actorID)
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	if manifest.BaseRevision == nil {
		return ArtifactGenerationResult{}, fmt.Errorf("manifest %s has no proposal base revision", manifest.ID)
	}
	base := core.VersionRef{
		ArtifactID: manifest.BaseRevision.ArtifactID, RevisionID: manifest.BaseRevision.RevisionID,
		ContentHash: manifest.BaseRevision.ContentHash,
	}
	if manifest.BaseRevision.AnchorID != "" {
		anchor := manifest.BaseRevision.AnchorID
		base.AnchorID = &anchor
	}
	if err := s.proposals.ValidateArtifactProposalBase(ctx, manifest.ProjectID, actorID, base); err != nil {
		return ArtifactGenerationResult{}, err
	}
	input, err := s.artifactInput(ctx, manifest)
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	baseContent, err := s.revisionContent(ctx, base.ArtifactID, base.RevisionID, base.ContentHash)
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	preflightSources, err := s.artifactPreflightSources(ctx, manifest)
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	result, output, err := s.generateValidatedArtifactOutput(ctx, manifest.ID, manifest.JobType, model, input, baseContent, preflightSources)
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	proposal, err := s.proposals.CreateProposal(ctx, manifest.ProjectID, actorID, core.CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: manifest.BaseRevision.ArtifactID,
		Operations: output.Operations, Assumptions: output.Assumptions, Questions: output.Questions,
		AIProvider: result.Provider, AIModel: result.Model,
	})
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	return ArtifactGenerationResult{Proposal: proposal, Provider: result.Provider, Model: result.Model, Usage: result.Usage}, nil
}

func (s *Service) generateValidatedArtifactOutput(
	ctx context.Context,
	runID string,
	jobType string,
	model string,
	input json.RawMessage,
	baseContent json.RawMessage,
	preflightSources []json.RawMessage,
) (ai.Result, artifactProposalOutput, error) {
	requestInput := input
	instructions := artifactProposalInstructions(jobType)
	var totalUsage *ai.Usage
	for pass := 0; pass < 2; pass++ {
		result, err := s.provider.Generate(ctx, ai.Request{
			RunID: runID, Model: model, Instructions: instructions,
			Input: requestInput, OutputSchema: artifactProposalSchema,
			OutputSchemaName: "artifact_patch_proposal", MaxOutputTokens: 32_768,
		})
		if err != nil {
			return ai.Result{}, artifactProposalOutput{}, err
		}
		totalUsage = combineAIUsage(totalUsage, result.Usage)
		output, validationErr := decodeArtifactProposalOutput(result.Output)
		if validationErr == nil {
			validationErr = preflightGeneratedArtifactProposal(
				jobType, baseContent, output.Operations, preflightSources...,
			)
		}
		if validationErr == nil {
			result.Usage = totalUsage
			return result, output, nil
		}
		if pass == 1 || jobType != "decompose_pages" || !errors.Is(validationErr, ai.ErrInvalidOutput) {
			return ai.Result{}, artifactProposalOutput{}, validationErr
		}
		requestInput, err = artifactProposalRepairInput(input, result.Output, validationErr)
		if err != nil {
			return ai.Result{}, artifactProposalOutput{}, err
		}
		instructions = artifactProposalRepairInstructions(jobType)
	}
	return ai.Result{}, artifactProposalOutput{}, ai.ErrInvalidOutput
}

func (s *Service) artifactPreflightSources(ctx context.Context, manifest domain.InputManifest) ([]json.RawMessage, error) {
	if manifest.JobType != "decompose_pages" {
		return nil, nil
	}
	if len(manifest.Sources) != 1 || strings.TrimSpace(manifest.Sources[0].Purpose) != "requirement_baseline" {
		return nil, fmt.Errorf("decompose_pages requires exactly one frozen Requirement Baseline source")
	}
	source := manifest.Sources[0]
	if strings.TrimSpace(source.Ref.AnchorID) != "" {
		return nil, fmt.Errorf("decompose_pages requires a whole Requirement Baseline source")
	}
	payload, err := s.revisionContent(
		ctx, source.Ref.ArtifactID, source.Ref.RevisionID, source.Ref.ContentHash,
	)
	if err != nil {
		return nil, err
	}
	return []json.RawMessage{payload}, nil
}

func artifactProposalRepairInput(
	input json.RawMessage,
	previousOutput json.RawMessage,
	validationErr error,
) (json.RawMessage, error) {
	var envelope map[string]any
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, err
	}
	var previous any
	if err := json.Unmarshal(previousOutput, &previous); err != nil {
		return nil, err
	}
	envelope["previousInvalidProposal"] = previous
	envelope["deterministicValidationFeedback"] = validationErr.Error()
	return json.Marshal(envelope)
}

func artifactProposalRepairInstructions(jobType string) string {
	return artifactProposalInstructions(jobType) + " This is the single deterministic repair pass. Read previousInvalidProposal and deterministicValidationFeedback as data, then return a complete corrected proposal against the original baseContent. Correct every listed blocker; do not patch the invalid candidate as a new base."
}

func combineAIUsage(current, next *ai.Usage) *ai.Usage {
	if current == nil && next == nil {
		return nil
	}
	combined := ai.Usage{}
	if current != nil {
		combined = *current
	}
	if next != nil {
		combined.InputTokens += next.InputTokens
		combined.OutputTokens += next.OutputTokens
		combined.TotalTokens += next.TotalTokens
	}
	return &combined
}

func preflightGeneratedArtifactProposal(
	jobType string,
	base json.RawMessage,
	operations []domain.ProposalOperation,
	preflightSources ...json.RawMessage,
) error {
	artifactKind := map[string]string{
		"derive_requirements": "product_requirements",
		"decompose_pages":     "blueprint",
		"generate_page_spec":  "page_spec",
		"generate_prototype":  "prototype",
	}[jobType]
	// Generated downstream artifacts feed deterministic lineage compilers and
	// fan-out/runtime consumers. Do not persist a proposal that can never pass
	// its next canonical boundary; invalid output lets the workflow retry without
	// creating a broken Proposal identity that a human could accidentally apply.
	if artifactKind == "" {
		return nil
	}
	accepted := make([]domain.ProposalOperation, len(operations))
	copy(accepted, operations)
	for index := range accepted {
		accepted[index].Decision = domain.DecisionAccepted
	}
	temporary := domain.OutputProposal{Status: domain.ProposalReady, Operations: accepted}
	ordered, err := temporary.AcceptedOperations()
	if err != nil {
		return fmt.Errorf("%w: %s dependency graph: %v", ai.ErrInvalidOutput, jobType, err)
	}
	candidate, err := domain.ApplyProposalPatch(base, ordered)
	if err != nil {
		return fmt.Errorf("%w: %s patch: %v", ai.ErrInvalidOutput, jobType, err)
	}
	report := core.ValidateArtifactContent(artifactKind, candidate)
	blockers := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		if finding.Severity == "blocker" {
			blockers = append(blockers, finding.Code+"@"+finding.Path+": "+finding.Message)
		}
	}
	if len(blockers) > 0 {
		sort.Strings(blockers)
		remaining := len(blockers) - min(len(blockers), 20)
		blockers = blockers[:min(len(blockers), 20)]
		feedback := strings.Join(blockers, ", ")
		if remaining > 0 {
			feedback += fmt.Sprintf(", ... (%d more blockers)", remaining)
		}
		return fmt.Errorf(
			"%w: %s proposal does not satisfy the canonical %s contract: %s",
			ai.ErrInvalidOutput, jobType, artifactKind, feedback,
		)
	}
	if jobType == "decompose_pages" && len(preflightSources) > 0 {
		if len(preflightSources) != 1 {
			return fmt.Errorf("%w: decompose_pages requires exactly one Requirement Baseline for trace validation", ai.ErrInvalidOutput)
		}
		if err := core.ValidateBlueprintAgainstRequirementBaseline(candidate, preflightSources[0], true); err != nil {
			return fmt.Errorf("%w: decompose_pages requirement trace: %v", ai.ErrInvalidOutput, err)
		}
	}
	return nil
}

func decodeArtifactProposalOutput(payload json.RawMessage) (artifactProposalOutput, error) {
	var wire artifactProposalAIOutput
	if err := json.Unmarshal(payload, &wire); err != nil {
		return artifactProposalOutput{}, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	output := artifactProposalOutput{
		Operations:  make([]domain.ProposalOperation, len(wire.Operations)),
		Assumptions: append([]string(nil), wire.Assumptions...),
		Questions:   append([]string(nil), wire.Questions...),
	}
	for index, operation := range wire.Operations {
		value, err := domain.CanonicalJSON(json.RawMessage(operation.ValueJSON))
		if err != nil {
			return artifactProposalOutput{}, fmt.Errorf(
				"%w: operations[%d].valueJson: %v", ai.ErrInvalidOutput, index, err,
			)
		}
		switch operation.Kind {
		case domain.OperationAdd, domain.OperationReplace:
		case domain.OperationRemove:
			if !bytes.Equal(value, []byte("null")) {
				return artifactProposalOutput{}, fmt.Errorf(
					"%w: operations[%d].valueJson must be null for remove", ai.ErrInvalidOutput, index,
				)
			}
			value = nil
		default:
			return artifactProposalOutput{}, fmt.Errorf(
				"%w: operations[%d].kind", ai.ErrInvalidOutput, index,
			)
		}
		output.Operations[index] = domain.ProposalOperation{
			ID: operation.ID, Kind: operation.Kind, Path: operation.Path, Value: value,
			DependsOn: append([]string(nil), operation.DependsOn...), Rationale: operation.Rationale,
		}
	}
	return output, nil
}

func (s *Service) GenerateImplementation(ctx context.Context, request ImplementationGenerationRequest) (ImplementationGenerationResult, error) {
	instruction, instructionJSON, instructionHash, err := canonicalImplementationInstruction(request.Instruction)
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	request.Instruction = instruction
	request.BundleID = strings.TrimSpace(request.BundleID)
	request.ActorID = strings.TrimSpace(request.ActorID)
	request.Model = strings.TrimSpace(request.Model)
	if request.BundleID == "" || request.ActorID == "" || request.Model == "" {
		return ImplementationGenerationResult{}, fmt.Errorf("%w: implementation generation identity", core.ErrInvalidInput)
	}
	if request.ExecutionSource == "" {
		request.ExecutionSource = core.ImplementationSourceManualGeneration
	}
	if request.ExecutionSource != core.ImplementationSourceManualGeneration &&
		request.ExecutionSource != core.ImplementationSourceWorkflowRunner &&
		request.ExecutionSource != core.ImplementationSourceConversationCommand {
		return ImplementationGenerationResult{}, fmt.Errorf("%w: implementation execution source", core.ErrInvalidInput)
	}
	request.ExpectedRunID = strings.TrimSpace(request.ExpectedRunID)
	request.ExpectedRootBundleID = strings.TrimSpace(request.ExpectedRootBundleID)
	if request.ExecutionSource == core.ImplementationSourceConversationCommand &&
		(request.ExpectedRunID == "" || request.ExpectedRootBundleID == "") {
		return ImplementationGenerationResult{}, fmt.Errorf("%w: conversation generation requires expected run and root bundle", core.ErrInvalidInput)
	}
	for label, value := range map[string]string{
		"expected run":         request.ExpectedRunID,
		"expected root bundle": request.ExpectedRootBundleID,
	} {
		if value != "" {
			if _, parseErr := uuid.Parse(value); parseErr != nil {
				return ImplementationGenerationResult{}, fmt.Errorf("%w: %s", core.ErrInvalidInput, label)
			}
		}
	}
	state, err := s.workbench.GetLineageState(ctx, request.BundleID, request.ActorID)
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	if request.ExpectedRootBundleID != "" && state.RootBundleID != request.ExpectedRootBundleID {
		return ImplementationGenerationResult{}, core.ErrConflict
	}
	if request.ExpectedRunID != "" && (state.ActiveBundle.WorkflowRunID == nil || *state.ActiveBundle.WorkflowRunID != request.ExpectedRunID) {
		return ImplementationGenerationResult{}, core.ErrConflict
	}
	bundle, err := s.workbench.GetBundleForGeneration(ctx, state.ActiveBundle.ID, request.ActorID)
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	replayIdentity := currentImplementationGenerationReplayIdentity(instructionJSON, instructionHash, request.Model)
	if matchingProposal := recoverMatchingImplementationProposal(state.CurrentProposal, request, instructionHash); matchingProposal != nil {
		if _, err := s.governanceImplementationInput(ctx, bundle.ProjectID, request); err != nil {
			return ImplementationGenerationResult{}, err
		}
		recovered, err := s.recoverCompletedImplementationClaim(ctx, bundle, state.RootBundleID, request, replayIdentity, *matchingProposal)
		if err != nil {
			return ImplementationGenerationResult{}, err
		}
		return ImplementationGenerationResult{Proposal: recovered, Provider: recovered.AIProvider, Model: recovered.AIModel}, nil
	}
	if state.CurrentProposal != nil {
		if !supersedableImplementationGenerationProposal(*state.CurrentProposal) {
			return ImplementationGenerationResult{}, ErrActiveImplementationProposal
		}
		if request.ExecutionSource == core.ImplementationSourceConversationCommand {
			request.ExpectedActiveProposalID = state.CurrentProposal.ID
			request.ExpectedActiveProposalVersion = state.CurrentProposal.Version
		} else if request.ExecutionSource != core.ImplementationSourceManualGeneration ||
			request.ExpectedActiveProposalID != state.CurrentProposal.ID ||
			request.ExpectedActiveProposalVersion != state.CurrentProposal.Version {
			return ImplementationGenerationResult{}, ErrActiveImplementationProposal
		}
	} else if request.ExpectedActiveProposalID != "" || request.ExpectedActiveProposalVersion != 0 {
		return ImplementationGenerationResult{}, core.ErrConflict
	}
	governanceInput, err := s.governanceImplementationInput(ctx, bundle.ProjectID, request)
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	claim, recoveredProposalID, err := s.acquireImplementationClaim(ctx, bundle, state.RootBundleID, request, replayIdentity)
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	if recoveredProposalID != uuid.Nil {
		recovered, getErr := s.implementation.Get(ctx, recoveredProposalID.String(), request.ActorID)
		if getErr != nil {
			return ImplementationGenerationResult{}, getErr
		}
		if matched := recoverMatchingImplementationProposal(&recovered, request, instructionHash); matched == nil {
			return ImplementationGenerationResult{}, core.ErrConflict
		}
		return ImplementationGenerationResult{Proposal: recovered, Provider: recovered.AIProvider, Model: recovered.AIModel}, nil
	}
	input, err := s.implementationInput(ctx, bundle, string(instructionJSON), governanceInput)
	if err != nil {
		s.failImplementationClaim(context.WithoutCancel(ctx), claim, classifyImplementationGenerationFailure(err))
		return ImplementationGenerationResult{}, err
	}
	result, err := s.provider.Generate(ctx, ai.Request{
		RunID: bundle.ID, Model: request.Model, Instructions: implementationInstructions,
		Input: input, OutputSchema: implementationProposalSchema,
		OutputSchemaName: "implementation_proposal", MaxOutputTokens: 65_536,
	})
	if err != nil {
		s.failImplementationClaim(context.WithoutCancel(ctx), claim, classifyImplementationGenerationFailure(err))
		return ImplementationGenerationResult{}, err
	}
	output, err := decodeImplementationProposalOutput(result.Output)
	if err != nil {
		s.failImplementationClaim(context.WithoutCancel(ctx), claim, "ai_invalid_output")
		return ImplementationGenerationResult{}, err
	}
	output.BuildManifestID = bundle.ID
	proposal, err := s.implementation.CreateGenerated(ctx, bundle.ProjectID, request.ActorID, output, core.GeneratedImplementationIdentity{
		ProposalID: claim.ReservedProposalID.String(), RequestKey: claim.RequestKey.String(),
		ExecutionSource: request.ExecutionSource, ConversationCommandID: request.ConversationCommandID,
		ExpectedActiveProposalID:      uuidStringPointer(claim.ExpectedActiveProposalID),
		ExpectedActiveProposalVersion: claim.ExpectedActiveProposalVersion,
		InstructionHash:               instructionHash, AIProvider: result.Provider, AIModel: result.Model,
		ClaimToken: claim.Token.String(),
	})
	if err != nil {
		s.failImplementationClaim(context.WithoutCancel(ctx), claim, classifyImplementationGenerationFailure(err))
		return ImplementationGenerationResult{}, err
	}
	return ImplementationGenerationResult{Proposal: proposal, Provider: result.Provider, Model: result.Model, Usage: result.Usage}, nil
}

func decodeImplementationProposalOutput(payload json.RawMessage) (core.CreateImplementationProposalInput, error) {
	var wire implementationProposalAIOutput
	if err := json.Unmarshal(payload, &wire); err != nil {
		return core.CreateImplementationProposalInput{}, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	decode := func(field string, values []string) ([]json.RawMessage, error) {
		result := make([]json.RawMessage, len(values))
		for index, value := range values {
			canonical, err := domain.CanonicalJSON(json.RawMessage(value))
			if err != nil {
				return nil, fmt.Errorf("%w: %s[%d]: %v", ai.ErrInvalidOutput, field, index, err)
			}
			if len(canonical) == 0 || canonical[0] != '{' {
				return nil, fmt.Errorf("%w: %s[%d] must encode a JSON object", ai.ErrInvalidOutput, field, index)
			}
			result[index] = canonical
		}
		return result, nil
	}
	routes, err := decode("routes", wire.Routes)
	if err != nil {
		return core.CreateImplementationProposalInput{}, err
	}
	apis, err := decode("apis", wire.APIs)
	if err != nil {
		return core.CreateImplementationProposalInput{}, err
	}
	migrations, err := decode("migrations", wire.Migrations)
	if err != nil {
		return core.CreateImplementationProposalInput{}, err
	}
	tests, err := decode("tests", wire.Tests)
	if err != nil {
		return core.CreateImplementationProposalInput{}, err
	}
	previews, err := decode("previews", wire.Previews)
	if err != nil {
		return core.CreateImplementationProposalInput{}, err
	}
	traceLinks, err := decode("traceLinks", wire.TraceLinks)
	if err != nil {
		return core.CreateImplementationProposalInput{}, err
	}
	return core.CreateImplementationProposalInput{
		Operations: append([]core.FileOperation(nil), wire.Operations...),
		Routes:     routes, APIs: apis, Migrations: migrations, Tests: tests,
		Previews: previews, TraceLinks: traceLinks,
		Diagnostics:        append([]core.ValidationFinding(nil), wire.Diagnostics...),
		Assumptions:        append([]string(nil), wire.Assumptions...),
		UnimplementedItems: append([]string(nil), wire.UnimplementedItems...),
	}, nil
}

func supersedableImplementationGenerationProposal(proposal core.ImplementationProposal) bool {
	if proposal.Status != "open" || proposal.Version == 0 || proposal.AppliedAt != nil ||
		proposal.ExecutionSource == core.ImplementationSourceConversationCommand {
		return false
	}
	for _, operation := range proposal.Operations {
		if operation.Decision != core.ImplementationPending {
			return false
		}
	}
	return true
}

type implementationGenerationClaim struct {
	ID                            uuid.UUID
	ProjectID                     uuid.UUID
	ActorID                       uuid.UUID
	BuildManifestID               uuid.UUID
	RequestKey                    uuid.UUID
	ReservedProposalID            uuid.UUID
	Token                         uuid.UUID
	ExpectedActiveProposalID      *uuid.UUID
	ExpectedActiveProposalVersion uint64
}

type implementationGenerationReplayIdentity struct {
	Instruction               json.RawMessage
	InstructionHash           string
	RequestedModel            string
	GenerationContractVersion string
	SystemPromptHash          string
	OutputSchemaHash          string
}

func currentImplementationGenerationReplayIdentity(
	instruction json.RawMessage,
	instructionHash string,
	requestedModel string,
) implementationGenerationReplayIdentity {
	return implementationGenerationReplayIdentity{
		Instruction:               append(json.RawMessage(nil), instruction...),
		InstructionHash:           instructionHash,
		RequestedModel:            strings.TrimSpace(requestedModel),
		GenerationContractVersion: implementationGenerationContractVersion,
		SystemPromptHash:          generationSHA256([]byte(implementationInstructions)),
		OutputSchemaHash:          generationSHA256(implementationProposalSchema),
	}
}

func CanonicalImplementationInstruction(
	objective string,
	constraints []string,
) (ImplementationInstruction, json.RawMessage, string, error) {
	return canonicalImplementationInstruction(ImplementationInstruction{Objective: objective, Constraints: constraints})
}

func canonicalImplementationInstruction(
	instruction ImplementationInstruction,
) (ImplementationInstruction, json.RawMessage, string, error) {
	instruction.Objective = strings.TrimSpace(instruction.Objective)
	if len(instruction.Objective) > 16_000 || len(instruction.Constraints) > 256 {
		return ImplementationInstruction{}, nil, "", fmt.Errorf("%w: implementation instruction", core.ErrInvalidInput)
	}
	constraints := make([]string, 0, len(instruction.Constraints))
	for _, value := range instruction.Constraints {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 4_000 {
			return ImplementationInstruction{}, nil, "", fmt.Errorf("%w: implementation constraint", core.ErrInvalidInput)
		}
		constraints = append(constraints, value)
	}
	if constraints == nil {
		constraints = []string{}
	}
	instruction.Constraints = constraints
	encoded, err := domain.CanonicalJSON(instruction)
	if err != nil {
		return ImplementationInstruction{}, nil, "", err
	}
	hash, err := domain.CanonicalHash(instruction)
	if err != nil {
		return ImplementationInstruction{}, nil, "", err
	}
	return instruction, encoded, "sha256:" + hash, nil
}

func recoverMatchingImplementationProposal(
	proposal *core.ImplementationProposal,
	request ImplementationGenerationRequest,
	instructionHash string,
) *core.ImplementationProposal {
	if proposal == nil {
		return nil
	}
	commandID := ""
	if request.ConversationCommandID != nil {
		commandID = strings.TrimSpace(*request.ConversationCommandID)
	}
	matchedIdentity := false
	if request.ExecutionSource == core.ImplementationSourceConversationCommand {
		matchedIdentity = commandID != "" && proposal.ConversationCommandID != nil &&
			*proposal.ConversationCommandID == commandID && proposal.ID == commandID
	} else if strings.TrimSpace(request.ProposalID) != "" {
		matchedIdentity = proposal.ID == strings.TrimSpace(request.ProposalID)
	}
	if !matchedIdentity || proposal.ExecutionSource != request.ExecutionSource ||
		proposal.InstructionHash != instructionHash ||
		(request.ExecutionSource != core.ImplementationSourceConversationCommand && proposal.CreatedBy != request.ActorID) ||
		(proposal.Status != "open" && proposal.Status != "reviewing" && proposal.Status != "ready") || proposal.AppliedAt != nil {
		return nil
	}
	copy := *proposal
	return &copy
}

func (s *Service) recoverCompletedImplementationClaim(
	ctx context.Context,
	bundle core.WorkbenchBundle,
	rootBundleID string,
	request ImplementationGenerationRequest,
	replayIdentity implementationGenerationReplayIdentity,
	proposal core.ImplementationProposal,
) (core.ImplementationProposal, error) {
	projectID, projectErr := uuid.Parse(bundle.ProjectID)
	leafID, leafErr := uuid.Parse(bundle.ID)
	rootID, rootErr := uuid.Parse(rootBundleID)
	actorID, actorErr := uuid.Parse(request.ActorID)
	requestKey, proposalID, commandID, identityErr := normalizeGenerationRequestIdentity(request)
	governanceManifestID, governanceManifestHash, governanceSourceRefs, governanceErr := generationGovernanceClaimIdentity(request)
	if projectErr != nil || leafErr != nil || rootErr != nil || actorErr != nil || identityErr != nil || governanceErr != nil {
		return core.ImplementationProposal{}, core.ErrConflict
	}
	var claim storage.ImplementationGenerationClaimModel
	if err := s.database.WithContext(ctx).Where("request_key = ?", requestKey).Take(&claim).Error; err != nil {
		return core.ImplementationProposal{}, core.ErrConflict
	}
	if claim.ProjectID != projectID || claim.BuildManifestID != leafID || claim.RootManifestID != rootID ||
		claim.ReservedProposalID != proposalID || claim.ExecutionSource != string(request.ExecutionSource) ||
		!sameOptionalUUID(claim.ConversationCommandID, commandID) ||
		!implementationGenerationGovernanceMatches(claim, governanceManifestID, governanceManifestHash, governanceSourceRefs) ||
		!implementationGenerationReplayMatches(claim, replayIdentity) ||
		!implementationGenerationActorCompatible(request.ExecutionSource, claim.ActorID, actorID) ||
		claim.Status != "completed" || claim.CompletedProposalID == nil || *claim.CompletedProposalID != proposalID ||
		claim.ClaimToken != nil || claim.ClaimExpiresAt != nil || proposal.ID != proposalID.String() {
		return core.ImplementationProposal{}, core.ErrConflict
	}
	if request.ExecutionSource != core.ImplementationSourceConversationCommand {
		var expectedID *uuid.UUID
		if strings.TrimSpace(request.ExpectedActiveProposalID) != "" {
			parsed, err := uuid.Parse(strings.TrimSpace(request.ExpectedActiveProposalID))
			if err != nil {
				return core.ImplementationProposal{}, core.ErrConflict
			}
			expectedID = &parsed
		}
		if !sameOptionalUUID(claim.ExpectedActiveProposalID, expectedID) ||
			!sameOptionalUint64(claim.ExpectedActiveProposalVersion, positiveVersionPointer(request.ExpectedActiveProposalVersion)) {
			return core.ImplementationProposal{}, core.ErrConflict
		}
	}
	return proposal, nil
}

func (s *Service) acquireImplementationClaim(
	ctx context.Context,
	bundle core.WorkbenchBundle,
	rootBundleID string,
	request ImplementationGenerationRequest,
	replayIdentity implementationGenerationReplayIdentity,
) (implementationGenerationClaim, uuid.UUID, error) {
	projectID, err := uuid.Parse(bundle.ProjectID)
	if err != nil {
		return implementationGenerationClaim{}, uuid.Nil, fmt.Errorf("%w: project id", core.ErrInvalidInput)
	}
	actorID, err := uuid.Parse(request.ActorID)
	if err != nil {
		return implementationGenerationClaim{}, uuid.Nil, fmt.Errorf("%w: actor id", core.ErrInvalidInput)
	}
	leafID, err := uuid.Parse(bundle.ID)
	if err != nil {
		return implementationGenerationClaim{}, uuid.Nil, fmt.Errorf("%w: build manifest id", core.ErrInvalidInput)
	}
	rootID, err := uuid.Parse(rootBundleID)
	if err != nil {
		return implementationGenerationClaim{}, uuid.Nil, fmt.Errorf("%w: root build manifest id", core.ErrInvalidInput)
	}
	requestKey, proposalID, commandID, err := normalizeGenerationRequestIdentity(request)
	if err != nil {
		return implementationGenerationClaim{}, uuid.Nil, err
	}
	governanceManifestID, governanceManifestHash, governanceSourceRefs, err := generationGovernanceClaimIdentity(request)
	if err != nil {
		return implementationGenerationClaim{}, uuid.Nil, err
	}
	now := s.now().UTC()
	token := uuid.New()
	expiresAt := now.Add(s.claimLease)
	var expectedActiveProposalID *uuid.UUID
	if strings.TrimSpace(request.ExpectedActiveProposalID) != "" {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(request.ExpectedActiveProposalID))
		if parseErr != nil || request.ExpectedActiveProposalVersion == 0 {
			return implementationGenerationClaim{}, uuid.Nil, fmt.Errorf("%w: expected active proposal", core.ErrInvalidInput)
		}
		expectedActiveProposalID = &parsed
	} else if request.ExpectedActiveProposalVersion != 0 {
		return implementationGenerationClaim{}, uuid.Nil, fmt.Errorf("%w: expected active proposal version", core.ErrInvalidInput)
	}
	claim := implementationGenerationClaim{
		ID: uuid.New(), ProjectID: projectID, ActorID: actorID,
		BuildManifestID: leafID, RequestKey: requestKey, ReservedProposalID: proposalID, Token: token,
		ExpectedActiveProposalID:      expectedActiveProposalID,
		ExpectedActiveProposalVersion: request.ExpectedActiveProposalVersion,
	}
	var recoveredProposalID uuid.UUID
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var root storage.ApplicationBuildManifestModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND project_id = ?", rootID, projectID,
		).Take(&root).Error; err != nil {
			return mapGenerationNotFound(err)
		}
		var leaf storage.ApplicationBuildManifestModel
		if leafID == rootID {
			leaf = root
		} else if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND project_id = ? AND root_manifest_id = ?", leafID, projectID, rootID,
		).Take(&leaf).Error; err != nil {
			return mapGenerationNotFound(err)
		}
		if leaf.Status != "frozen" || leaf.RootManifestID != rootID {
			return core.ErrConflict
		}
		if request.ExpectedRunID != "" {
			expectedRunID, parseErr := uuid.Parse(strings.TrimSpace(request.ExpectedRunID))
			if parseErr != nil || leaf.WorkflowRunID == nil || *leaf.WorkflowRunID != expectedRunID {
				return core.ErrConflict
			}
		}
		var childCount int64
		if err := transaction.Model(&storage.ApplicationBuildManifestModel{}).
			Where("derived_from_id = ?", leafID).Count(&childCount).Error; err != nil {
			return err
		}
		if childCount != 0 {
			return core.ErrProposalStale
		}

		var existingRequest storage.ImplementationGenerationClaimModel
		existingRequestErr := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_key = ?", requestKey).Take(&existingRequest).Error
		if existingRequestErr == nil {
			if existingRequest.ProjectID != projectID || existingRequest.BuildManifestID != leafID ||
				existingRequest.RootManifestID != rootID || existingRequest.ReservedProposalID != proposalID ||
				existingRequest.ExecutionSource != string(request.ExecutionSource) ||
				!sameOptionalUUID(existingRequest.ConversationCommandID, commandID) ||
				!implementationGenerationGovernanceMatches(existingRequest, governanceManifestID, governanceManifestHash, governanceSourceRefs) ||
				!implementationGenerationReplayMatches(existingRequest, replayIdentity) ||
				!sameOptionalUUID(existingRequest.ExpectedActiveProposalID, expectedActiveProposalID) ||
				!sameOptionalUint64(existingRequest.ExpectedActiveProposalVersion, positiveVersionPointer(request.ExpectedActiveProposalVersion)) ||
				!implementationGenerationActorCompatible(request.ExecutionSource, existingRequest.ActorID, actorID) {
				return core.ErrConflict
			}
			claim.ID = existingRequest.ID
			if existingRequest.Status == "completed" && existingRequest.CompletedProposalID != nil {
				recoveredProposalID = *existingRequest.CompletedProposalID
				return nil
			}
			if existingRequest.Status == "processing" && existingRequest.ClaimExpiresAt != nil && existingRequest.ClaimExpiresAt.After(now) {
				return ErrImplementationGenerationProcessing
			}
		} else if !errors.Is(existingRequestErr, gorm.ErrRecordNotFound) {
			return existingRequestErr
		}

		var otherProcessing storage.ImplementationGenerationClaimModel
		otherErr := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"build_manifest_id = ? AND status = 'processing' AND request_key <> ?", leafID, requestKey,
		).Take(&otherProcessing).Error
		if otherErr == nil {
			if otherProcessing.ClaimExpiresAt != nil && otherProcessing.ClaimExpiresAt.After(now) {
				return ErrImplementationGenerationProcessing
			}
			failure := "conflict"
			if err := transaction.Model(&storage.ImplementationGenerationClaimModel{}).Where(
				"id = ? AND status = 'processing'", otherProcessing.ID,
			).Updates(map[string]any{
				"status": "failed", "claim_token": nil, "claim_expires_at": nil,
				"last_failure": failure, "last_failed_at": now, "updated_at": now,
			}).Error; err != nil {
				return err
			}
		} else if !errors.Is(otherErr, gorm.ErrRecordNotFound) {
			return otherErr
		}

		var active storage.ImplementationProposalModel
		activeErr := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"build_manifest_id = ? AND status IN ?", leafID, []string{"open", "reviewing", "ready"},
		).Take(&active).Error
		if activeErr == nil {
			if expectedActiveProposalID == nil || active.ID != *expectedActiveProposalID ||
				active.Version != request.ExpectedActiveProposalVersion || active.Status != "open" ||
				active.AcceptedCount != 0 || active.RejectedCount != 0 || active.AppliedAt != nil {
				return ErrActiveImplementationProposal
			}
			if active.ExecutionSource == string(core.ImplementationSourceConversationCommand) {
				return ErrActiveImplementationProposal
			}
		}
		if activeErr != nil && !errors.Is(activeErr, gorm.ErrRecordNotFound) {
			return activeErr
		}
		if errors.Is(activeErr, gorm.ErrRecordNotFound) && expectedActiveProposalID != nil {
			return core.ErrConflict
		}

		if existingRequestErr == nil {
			updates := map[string]any{
				"actor_id": actorID, "claim_token": token, "claim_expires_at": expiresAt, "status": "processing",
				"attempt_count": existingRequest.AttemptCount + 1, "completed_proposal_id": nil,
				"last_failure": nil, "last_failed_at": nil, "updated_at": now,
			}
			result := transaction.Model(&storage.ImplementationGenerationClaimModel{}).
				Where("id = ? AND status <> 'completed'", existingRequest.ID).Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return core.ErrConflict
			}
		} else {
			current := storage.ImplementationGenerationClaimModel{
				ID:              claim.ID,
				BuildManifestID: leafID, ProjectID: projectID, RootManifestID: rootID,
				RequestKey: requestKey, ReservedProposalID: proposalID,
				ExecutionSource: string(request.ExecutionSource), ConversationCommandID: commandID,
				GovernanceManifestID:      governanceManifestID,
				GovernanceManifestHash:    nonEmptyGenerationString(governanceManifestHash),
				GovernanceSourceRefs:      governanceSourceRefs,
				Instruction:               append(json.RawMessage(nil), replayIdentity.Instruction...),
				InstructionHash:           replayIdentity.InstructionHash,
				RequestedModel:            replayIdentity.RequestedModel,
				GenerationContractVersion: replayIdentity.GenerationContractVersion,
				SystemPromptHash:          replayIdentity.SystemPromptHash,
				OutputSchemaHash:          replayIdentity.OutputSchemaHash,
				ActorID:                   actorID, ClaimToken: &token,
				ExpectedActiveProposalID:      expectedActiveProposalID,
				ExpectedActiveProposalVersion: positiveVersionPointer(request.ExpectedActiveProposalVersion),
				ClaimExpiresAt:                &expiresAt, Status: "processing", AttemptCount: 1,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&current).Error; err != nil {
				return err
			}
		}
		if err := generationAudit(transaction, projectID, actorID, "implementation.generation_claimed", leafID.String(), map[string]any{
			"rootBuildManifestId": rootID.String(), "requestKey": requestKey.String(),
			"reservedProposalId": proposalID.String(), "executionSource": request.ExecutionSource,
			"conversationCommandId": uuidPointerString(commandID), "instructionHash": replayIdentity.InstructionHash,
			"requestedModel": replayIdentity.RequestedModel, "generationContractVersion": replayIdentity.GenerationContractVersion,
			"systemPromptHash": replayIdentity.SystemPromptHash, "outputSchemaHash": replayIdentity.OutputSchemaHash,
			"expectedActiveProposalId":      uuidPointerString(expectedActiveProposalID),
			"expectedActiveProposalVersion": request.ExpectedActiveProposalVersion,
			"governanceManifestId":          uuidPointerString(governanceManifestID),
			"governanceManifestHash":        governanceManifestHash,
			"claimExpiresAt":                expiresAt,
		}); err != nil {
			return err
		}
		return generationOutbox(transaction, leafID.String(), "implementation.generation_claimed", map[string]any{
			"projectId": projectID.String(), "buildManifestId": leafID.String(),
			"requestKey": requestKey.String(), "executionSource": request.ExecutionSource,
		})
	})
	if err != nil {
		return implementationGenerationClaim{}, uuid.Nil, err
	}
	return claim, recoveredProposalID, nil
}

func normalizeGenerationRequestIdentity(
	request ImplementationGenerationRequest,
) (uuid.UUID, uuid.UUID, *uuid.UUID, error) {
	var commandID *uuid.UUID
	if request.ConversationCommandID != nil {
		parsed, err := uuid.Parse(strings.TrimSpace(*request.ConversationCommandID))
		if err != nil {
			return uuid.Nil, uuid.Nil, nil, fmt.Errorf("%w: conversation command id", core.ErrInvalidInput)
		}
		commandID = &parsed
	}
	if request.ExecutionSource == core.ImplementationSourceConversationCommand {
		if commandID == nil {
			return uuid.Nil, uuid.Nil, nil, fmt.Errorf("%w: conversation command identity", core.ErrInvalidInput)
		}
		requestKey, keyErr := uuid.Parse(strings.TrimSpace(request.RequestKey))
		proposalID, proposalErr := uuid.Parse(strings.TrimSpace(request.ProposalID))
		if keyErr != nil || proposalErr != nil || requestKey != *commandID || proposalID != *commandID {
			return uuid.Nil, uuid.Nil, nil, fmt.Errorf("%w: deterministic conversation proposal identity", core.ErrInvalidInput)
		}
		return requestKey, proposalID, commandID, nil
	}
	if commandID != nil {
		return uuid.Nil, uuid.Nil, nil, fmt.Errorf("%w: non-conversation generation command", core.ErrInvalidInput)
	}
	requestKey := uuid.New()
	if strings.TrimSpace(request.RequestKey) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(request.RequestKey))
		if err != nil {
			return uuid.Nil, uuid.Nil, nil, fmt.Errorf("%w: generation request key", core.ErrInvalidInput)
		}
		requestKey = parsed
	}
	proposalID := uuid.New()
	if strings.TrimSpace(request.ProposalID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(request.ProposalID))
		if err != nil {
			return uuid.Nil, uuid.Nil, nil, fmt.Errorf("%w: reserved proposal id", core.ErrInvalidInput)
		}
		proposalID = parsed
	}
	return requestKey, proposalID, nil, nil
}

func generationGovernanceClaimIdentity(
	request ImplementationGenerationRequest,
) (*uuid.UUID, string, json.RawMessage, error) {
	if request.ExecutionSource != core.ImplementationSourceConversationCommand {
		if request.GovernanceManifest != nil || len(request.GovernanceSourceRefs) != 0 {
			return nil, "", nil, fmt.Errorf("%w: non-conversation governance input", core.ErrInvalidInput)
		}
		return nil, "", nil, nil
	}
	if request.GovernanceManifest == nil || len(request.GovernanceSourceRefs) == 0 {
		return nil, "", nil, fmt.Errorf("%w: conversation governance claim", core.ErrInvalidInput)
	}
	manifestID, err := uuid.Parse(strings.TrimSpace(request.GovernanceManifest.ID))
	if err != nil || !validGenerationSHA256(request.GovernanceManifest.Hash) {
		return nil, "", nil, fmt.Errorf("%w: governance manifest ref", core.ErrInvalidInput)
	}
	encoded, err := domain.CanonicalJSON(request.GovernanceSourceRefs)
	if err != nil {
		return nil, "", nil, err
	}
	return &manifestID, request.GovernanceManifest.Hash, encoded, nil
}

func (s *Service) failImplementationClaim(ctx context.Context, claim implementationGenerationClaim, failure string) {
	if claim.Token == uuid.Nil || failure == "" {
		return
	}
	now := s.now().UTC()
	_ = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&storage.ImplementationGenerationClaimModel{}).Where(
			"id = ? AND request_key = ? AND claim_token = ? AND status = 'processing'",
			claim.ID, claim.RequestKey, claim.Token,
		).Updates(map[string]any{
			"status": "failed", "claim_token": nil, "claim_expires_at": nil,
			"last_failure": failure, "last_failed_at": now, "updated_at": now,
		})
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		if err := generationAudit(transaction, claim.ProjectID, claim.ActorID, "implementation.generation_failed", claim.BuildManifestID.String(), map[string]any{
			"requestKey": claim.RequestKey.String(), "failureClass": failure,
		}); err != nil {
			return err
		}
		return generationOutbox(transaction, claim.BuildManifestID.String(), "implementation.generation_failed", map[string]any{
			"buildManifestId": claim.BuildManifestID.String(), "requestKey": claim.RequestKey.String(), "failureClass": failure,
		})
	})
}

func SafeImplementationFailureClass(err error) string {
	return classifyImplementationGenerationFailure(err)
}

func classifyImplementationGenerationFailure(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, core.ErrNotFound):
		return "not_found"
	case errors.Is(err, core.ErrForbidden):
		return "forbidden"
	case errors.Is(err, core.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, core.ErrProposalStale):
		return "proposal_stale"
	case errors.Is(err, core.ErrBlockingGate):
		return "blocking_gate"
	case errors.Is(err, core.ErrContentNotReady):
		return "content_not_ready"
	case errors.Is(err, ai.ErrNotConfigured):
		return "ai_not_configured"
	case errors.Is(err, ai.ErrRateLimited):
		return "ai_rate_limited"
	case errors.Is(err, ai.ErrUnavailable):
		return "ai_unavailable"
	case errors.Is(err, ai.ErrInvalidOutput):
		return "ai_invalid_output"
	case errors.Is(err, ai.ErrContextTooLong):
		return "ai_context_too_long"
	case errors.Is(err, core.ErrConflict), errors.Is(err, ErrActiveImplementationProposal), errors.Is(err, ErrImplementationGenerationProcessing):
		return "conflict"
	default:
		return "internal"
	}
}

func mapGenerationNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return core.ErrNotFound
	}
	return err
}

func generationAudit(transaction *gorm.DB, projectID, actorID uuid.UUID, action, targetID string, metadata map[string]any) error {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var requestID *string
	if value := core.RequestIDFromContext(transaction.Statement.Context); value != "" {
		requestID = &value
	}
	return transaction.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: &projectID, ActorID: &actorID, RequestID: requestID,
		Action: action, TargetType: "implementation_generation", TargetID: targetID,
		Metadata: payload, CreatedAt: time.Now().UTC(),
	}).Error
}

func generationOutbox(transaction *gorm.DB, aggregateID, eventType string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return transaction.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: "implementation_generation", AggregateID: aggregateID,
		EventType: eventType, Subject: "worksflow." + strings.ReplaceAll(eventType, "_", "."),
		Payload: encoded, Headers: json.RawMessage(`{}`), AvailableAt: now, CreatedAt: now,
	}).Error
}

func uuidPointerString(value *uuid.UUID) any {
	if value == nil {
		return nil
	}
	return value.String()
}

func uuidStringPointer(value *uuid.UUID) *string {
	if value == nil {
		return nil
	}
	result := value.String()
	return &result
}

func sameOptionalUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalUint64(left, right *uint64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func nonEmptyGenerationString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullableGenerationJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func implementationGenerationReplayMatches(
	existing storage.ImplementationGenerationClaimModel,
	requested implementationGenerationReplayIdentity,
) bool {
	return jsonBytesEqual(existing.Instruction, requested.Instruction) &&
		existing.InstructionHash == requested.InstructionHash &&
		existing.RequestedModel == requested.RequestedModel &&
		existing.GenerationContractVersion == requested.GenerationContractVersion &&
		existing.SystemPromptHash == requested.SystemPromptHash &&
		existing.OutputSchemaHash == requested.OutputSchemaHash
}

func implementationGenerationGovernanceMatches(
	existing storage.ImplementationGenerationClaimModel,
	manifestID *uuid.UUID,
	manifestHash string,
	sourceRefs json.RawMessage,
) bool {
	return sameOptionalUUID(existing.GovernanceManifestID, manifestID) &&
		stringPointerValue(existing.GovernanceManifestHash) == manifestHash &&
		jsonBytesEqual(existing.GovernanceSourceRefs, sourceRefs)
}

func implementationGenerationActorCompatible(
	source core.ImplementationExecutionSource,
	existing uuid.UUID,
	requested uuid.UUID,
) bool {
	return existing == requested || source == core.ImplementationSourceConversationCommand
}

func jsonBytesEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || string(left) == "null" {
		left = nil
	}
	if len(right) == 0 || string(right) == "null" {
		right = nil
	}
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	canonicalLeft, leftErr := domain.CanonicalJSON(left)
	canonicalRight, rightErr := domain.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(canonicalLeft, canonicalRight)
}

func generationSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validGenerationSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func positiveVersionPointer(value uint64) *uint64 {
	if value == 0 {
		return nil
	}
	return &value
}

func (s *Service) artifactInput(ctx context.Context, manifest domain.InputManifest) (json.RawMessage, error) {
	type sourceContent struct {
		Ref     domain.ArtifactRef `json:"ref"`
		Purpose string             `json:"purpose"`
		Content any                `json:"content"`
	}
	sources := make([]sourceContent, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		payload, err := s.revisionContent(ctx, source.Ref.ArtifactID, source.Ref.RevisionID, source.Ref.ContentHash)
		if err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		sources = append(sources, sourceContent{Ref: source.Ref, Purpose: source.Purpose, Content: redact(value, "")})
	}
	var baseContent any
	if manifest.BaseRevision != nil {
		payload, err := s.revisionContent(ctx, manifest.BaseRevision.ArtifactID, manifest.BaseRevision.RevisionID, manifest.BaseRevision.ContentHash)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &baseContent); err != nil {
			return nil, err
		}
		baseContent = redact(baseContent, "")
	}
	return json.Marshal(map[string]any{
		"inputManifest": manifest, "baseContent": baseContent, "sources": sources,
	})
}

func (s *Service) implementationInput(ctx context.Context, bundle core.WorkbenchBundle, instruction string, governanceInput json.RawMessage) (json.RawMessage, error) {
	sourceContents := make([]map[string]any, 0)
	refs := []core.VersionRef{bundle.BlueprintRevision, bundle.PageSpecRevision, bundle.PrototypeRevision}
	refs = append(refs, bundle.RequirementRevisions...)
	refs = append(refs, bundle.ContractRevisions...)
	refs = append(refs, bundle.DesignSystemRevisions...)
	for _, reference := range refs {
		payload, err := s.revisionContent(ctx, reference.ArtifactID, reference.RevisionID, reference.ContentHash)
		if err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		sourceContents = append(sourceContents, map[string]any{
			"version": reference, "content": redact(value, ""),
		})
	}
	for _, contextRevision := range bundle.ContextRevisions {
		payload, err := s.revisionContent(
			ctx, contextRevision.Revision.ArtifactID, contextRevision.Revision.RevisionID, contextRevision.Revision.ContentHash,
		)
		if err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		sourceContents = append(sourceContents, map[string]any{
			"kind": contextRevision.Kind, "version": contextRevision.Revision, "content": redact(value, ""),
		})
	}
	var workspace any
	if bundle.CurrentWorkspaceRevision != nil {
		payload, err := s.revisionContent(ctx, bundle.CurrentWorkspaceRevision.ArtifactID, bundle.CurrentWorkspaceRevision.RevisionID, bundle.CurrentWorkspaceRevision.ContentHash)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &workspace); err != nil {
			return nil, err
		}
		workspace = workspaceWithFileHashes(workspace)
	}
	var workflowInput any
	if bundle.WorkflowContext != nil {
		encoded, err := s.artifactInput(ctx, bundle.WorkflowContext.InputManifest)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &workflowInput); err != nil {
			return nil, err
		}
	}
	return marshalImplementationInput(bundle, instruction, sourceContents, workflowInput, workspace, governanceInput)
}

func marshalImplementationInput(
	bundle core.WorkbenchBundle,
	instruction string,
	sourceContents []map[string]any,
	workflowInput any,
	workspace any,
	governanceInput json.RawMessage,
) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"applicationBuildManifest": bundle,
		"instruction":              strings.TrimSpace(instruction),
		"sourceContents":           sourceContents,
		"workflowInput":            workflowInput,
		"currentWorkspace":         workspace,
		"governanceInput":          governanceInput,
	})
}

func (s *Service) governanceImplementationInput(
	ctx context.Context,
	projectID string,
	request ImplementationGenerationRequest,
) (json.RawMessage, error) {
	if request.ExecutionSource != core.ImplementationSourceConversationCommand {
		if request.GovernanceManifest != nil || len(request.GovernanceSourceRefs) != 0 {
			return nil, fmt.Errorf("%w: supplemental governance input", core.ErrInvalidInput)
		}
		return nil, nil
	}
	if request.GovernanceManifest == nil || len(request.GovernanceSourceRefs) == 0 {
		return nil, fmt.Errorf("%w: conversation governance input", core.ErrInvalidInput)
	}
	manifest, err := s.proposals.GetManifest(ctx, request.GovernanceManifest.ID, request.ActorID)
	if err != nil {
		return nil, err
	}
	if manifest.ProjectID != projectID || manifest.Ref() != *request.GovernanceManifest {
		return nil, domain.ErrManifestUnpinned
	}
	expected := make(map[string]struct{}, len(manifest.Sources)+1)
	if manifest.BaseRevision != nil {
		expected[generationArtifactRefKey(*manifest.BaseRevision)] = struct{}{}
	}
	for _, source := range manifest.Sources {
		expected[generationArtifactRefKey(source.Ref)] = struct{}{}
	}
	actual := make(map[string]struct{}, len(request.GovernanceSourceRefs))
	for _, source := range request.GovernanceSourceRefs {
		key := generationArtifactRefKey(source)
		if _, duplicate := actual[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate governance source", core.ErrInvalidInput)
		}
		actual[key] = struct{}{}
	}
	if len(actual) != len(expected) {
		return nil, domain.ErrManifestUnpinned
	}
	for key := range expected {
		if _, exists := actual[key]; !exists {
			return nil, domain.ErrManifestUnpinned
		}
	}
	return s.artifactInput(ctx, manifest)
}

func generationArtifactRefKey(ref domain.ArtifactRef) string {
	return ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" + ref.AnchorID
}

func (s *Service) revisionContent(ctx context.Context, artifactID, revisionID, expectedHash string) (json.RawMessage, error) {
	parsedArtifact, err := uuid.Parse(artifactID)
	if err != nil {
		return nil, err
	}
	parsedRevision, err := uuid.Parse(revisionID)
	if err != nil {
		return nil, err
	}
	var revision storage.ArtifactRevisionModel
	err = s.database.WithContext(ctx).
		Where("id = ? AND artifact_id = ? AND content_hash = ?", parsedRevision, parsedArtifact, expectedHash).
		Take(&revision).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, core.ErrConflict
	}
	if err != nil {
		return nil, err
	}
	stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
	if err != nil {
		return nil, err
	}
	return stored.Payload, nil
}

func redact(value any, key string) any {
	if sensitiveCredentialKey(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, child := range typed {
			result[childKey] = redact(child, childKey)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = redact(child, key)
		}
		return result
	default:
		return value
	}
}

func sensitiveCredentialKey(key string) bool {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(key)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	_, sensitive := sensitiveCredentialKeys[normalized.String()]
	return sensitive
}

func workspaceWithFileHashes(value any) any {
	workspace, ok := value.(map[string]any)
	if !ok {
		return value
	}
	files, ok := workspace["files"].([]any)
	if !ok {
		return workspace
	}
	for _, item := range files {
		file, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, _ := file["content"].(string)
		digest := sha256.Sum256([]byte(content))
		file["contentHash"] = "sha256:" + hex.EncodeToString(digest[:])
	}
	return workspace
}

func artifactProposalInstructions(jobType string) string {
	instructions := []string{
		"You are an artifact transformation worker in a governed product-development workflow.",
		"The input manifest and all source versions are immutable data. Never claim that you changed canonical data.",
		"Return an RFC 6901 JSON-pointer patch proposal against baseContent.",
		"For add and replace operations, valueJson must contain the complete JSON value encoded as text; JSON strings therefore include their own quotes. For remove operations, valueJson must be the literal text null.",
		"Preserve human-authored content unless the requested transformation requires a precise change.",
		"Use stable requirement, node, state, layer, and operation IDs. Do not invent server UUIDs.",
		"Every operation must have a unique client operation ID, explicit dependencies, and a concise rationale.",
		"Record uncertainty as assumptions or questions instead of silently guessing.",
	}
	if contract := artifactProposalJobContract(jobType); contract != "" {
		instructions = append(instructions, contract)
	}
	instructions = append(instructions, "Job type: "+jobType+".")
	return strings.Join(instructions, " ")
}

func artifactProposalJobContract(jobType string) string {
	switch jobType {
	case "refine_project_brief":
		return "The fully applied Project Brief must have a non-empty top-level summary and at least one goal block with non-empty text. Never invent an answer to a blocking open question; preserve it for the human editor unless an immutable source contains the answer."
	case "derive_requirements":
		return "The fully applied Product Requirements must use the canonical top-level summary, blocks, requirements, and acceptanceCriteria fields. Add at least one stable source-context block. Every requirements item needs a stable id, non-empty statement, priority, sourceBlockIds that reference existing blocks, and acceptanceCriterionIds that reference existing acceptanceCriteria items; every Must requirement must reference at least one criterion. Every acceptanceCriteria item needs a unique stable id and non-empty statement. Do not encode requirement-to-criterion relationships only inside data or only as embedded objects because downstream baseline compilation consumes the canonical ID arrays. Cover every Must requirement ID, not only the requirements that already have source criteria."
	case "decompose_pages":
		return "The fully applied Blueprint must use the top-level nodes and edges arrays as its only semantic graph; never add a semantic alias or a second graph representation. It must contain at least one application Page. Every node needs a unique id, key or businessKey, and supported kind or type. Every Page needs a non-empty title, a unique absolute route, a non-empty userGoal, at least one requirementId copied exactly from the frozen Requirement Baseline, and a contains edge from a Feature. Collectively, Blueprint node requirementIds must cover every Must requirement ID from that baseline; never invent shorthand IDs such as REQ-001. Every API operation needs a supported method, an absolute path, and a requires edge to a Permission node. Use this canonical shape: {\"nodes\":[{\"id\":\"feature-main\",\"key\":\"FEATURE-MAIN\",\"kind\":\"feature\",\"title\":\"Main\"},{\"id\":\"page-main\",\"key\":\"PAGE-MAIN\",\"kind\":\"page\",\"title\":\"Main\",\"route\":\"/\",\"userGoal\":\"Complete the primary task\",\"requirementIds\":[\"exact-baseline-id\"]}],\"edges\":[{\"id\":\"edge-main-page\",\"sourceNodeId\":\"feature-main\",\"targetNodeId\":\"page-main\",\"kind\":\"contains\"}]}."
	case "generate_page_spec":
		return "The fully applied PageSpec must preserve the exact blueprintPageNodeId and provide a title, absolute route, user goal, and acceptance-criterion trace. Declare ready, loading, empty, and error states with unique stable IDs and keys, non-empty titles, and required set to true. Data bindings and interactions, when present, must use stable unique IDs and valid references."
	case "generate_prototype":
		return "The fully applied Prototype must preserve the exact pageSpecRevision, set exploratory to false, and reproduce the PageSpec state set exactly with identical stable IDs and keys, non-empty titles, explicit required flags, and fixtureIds arrays that exactly match each source state. Never invent a fixture or interaction: copy only fixtures and interactions declared by the PageSpec, and emit empty fixtures and interactions arrays when their PageSpec collections are empty. Every fixture must preserve its PageSpec state ownership and declare a name, response, integer HTTP statusCode from 100 through 599, nonnegative integer latencyMs, sanitized true, and a canonical sha256 contentHash; operationId is allowed only when it names the exact PageSpec API binding operation. Every interaction must preserve its PageSpec ID and trigger, reference an existing sourceLayerId, and contain at least one declarative action using only navigate, setState, openOverlay, closeOverlay, updateBinding, or submitFixture with exact declared references. Use a stable semantic layer object record keyed by layer ID, not a layer array; every record value must repeat its matching id and use valid parentId and childIds references. Provide exactly the desktop, tablet, and mobile breakpoints and exactly one valid frame for every required state and breakpoint pair, referencing an existing root layer. Do not invent componentRef, traceLinks, assets, overrides, tokenBindings, or componentBindings; keep those collections empty unless an immutable source supplies every exact governed reference."
	default:
		return ""
	}
}

const implementationGenerationContractVersion = "implementation-proposal-generation/v1"

const implementationInstructions = "You are an application implementation worker. Consume only the frozen ApplicationBuildManifest and pinned source contents. Return a reviewable implementation proposal; never claim to have written files. Use safe relative paths and never generate .env, credentials, .git, dependency caches, or build output. For every existing file update/delete/rename, copy its exact contentHash into expectedHash. New files must use an empty expectedHash. Include routes, APIs, migrations, tests, preview expectations, trace links, diagnostics, assumptions, and explicit unimplemented items. Every item in routes, apis, migrations, tests, previews, and traceLinks must be a JSON string that encodes exactly one object; the server decodes these transport strings before review. Generate tests for acceptance criteria and preserve human workspace changes. The proposed workspace must be reproducibly buildable by the governed quality sandbox: prefer dependency-free static HTML/CSS/JavaScript when a framework is unnecessary; otherwise use npm with a genuine package-lock.json that is consistent with package.json and an npm build script that emits a static dist/, out/, or build/ directory containing index.html. Never invent lockfile integrity values, rely on a CDN at build/runtime, or claim a dynamic server-only output is publishable as a static artifact. When a generated application needs the platform public data plane, read PUBLIC_WORKSFLOW_DATA_API_BASE and PUBLIC_WORKSFLOW_DATA_CAPABILITY only from window.__WORKSFLOW_ENV__ at runtime, send the capability as a Bearer token, and handle denied table/field policies explicitly. Never hardcode or persist a deployment capability in source, build artifacts, logs, or browser storage."

var artifactProposalSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["operations","assumptions","questions"],
  "properties":{
    "operations":{
      "type":"array","minItems":1,"maxItems":5000,
      "items":{
        "type":"object","additionalProperties":false,
        "required":["id","kind","path","valueJson","dependsOn","rationale"],
        "properties":{
          "id":{"type":"string","minLength":1,"maxLength":120},
          "kind":{"type":"string","enum":["add","replace","remove"]},
          "path":{"type":"string","maxLength":2048},
          "valueJson":{"type":"string","minLength":1},
          "dependsOn":{"type":"array","items":{"type":"string"},"maxItems":100},
          "rationale":{"type":"string","maxLength":2000}
        }
      }
    },
    "assumptions":{"type":"array","items":{"type":"string"},"maxItems":200},
    "questions":{"type":"array","items":{"type":"string"},"maxItems":200}
  }
}`)

var implementationProposalSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["operations","routes","apis","migrations","tests","previews","traceLinks","diagnostics","assumptions","unimplementedItems"],
  "properties":{
    "operations":{
      "type":"array","minItems":1,"maxItems":10000,
      "items":{
        "type":"object","additionalProperties":false,
        "required":["id","kind","path","fromPath","content","language","expectedHash","dependsOn","rationale","traceSource"],
        "properties":{
          "id":{"type":"string","minLength":1,"maxLength":120},
          "kind":{"type":"string","enum":["file.upsert","file.delete","file.rename"]},
          "path":{"type":"string","minLength":1,"maxLength":512},
          "fromPath":{"type":"string","maxLength":512},
          "content":{"type":["string","null"]},
          "language":{"type":"string","maxLength":80},
          "expectedHash":{"type":"string","maxLength":80},
          "dependsOn":{"type":"array","items":{"type":"string"},"maxItems":100},
          "rationale":{"type":"string","maxLength":2000},
          "traceSource":{"type":"array","items":{"type":"string"},"maxItems":500}
        }
      }
    },
    "routes":{"type":"array","items":{"type":"string","minLength":2}},
    "apis":{"type":"array","items":{"type":"string","minLength":2}},
    "migrations":{"type":"array","items":{"type":"string","minLength":2}},
    "tests":{"type":"array","items":{"type":"string","minLength":2}},
    "previews":{"type":"array","items":{"type":"string","minLength":2}},
    "traceLinks":{"type":"array","items":{"type":"string","minLength":2}},
    "diagnostics":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["code","path","message","severity"],"properties":{"code":{"type":"string"},"path":{"type":"string"},"message":{"type":"string"},"severity":{"type":"string","enum":["info","warning","blocker"]}}}},
    "assumptions":{"type":"array","items":{"type":"string"}},
    "unimplementedItems":{"type":"array","items":{"type":"string"}}
  }
}`)
