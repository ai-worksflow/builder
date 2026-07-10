package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

var sensitiveKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|private[_-]?key|authorization|cookie)`)

type ArtifactGenerationResult struct {
	Proposal coreProposal `json:"proposal"`
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	Usage    *ai.Usage    `json:"usage,omitempty"`
}

// coreProposal avoids hiding the domain contract while keeping this package's
// result stable if transport adds presentation fields.
type coreProposal = domain.OutputProposal

type ImplementationGenerationResult struct {
	Proposal core.ImplementationProposal `json:"proposal"`
	Provider string                      `json:"provider"`
	Model    string                      `json:"model"`
	Usage    *ai.Usage                   `json:"usage,omitempty"`
}

type Service struct {
	database       *gorm.DB
	contents       content.Store
	provider       ai.Provider
	proposals      *core.ProposalService
	workbench      *core.WorkbenchService
	implementation *core.ImplementationService
}

func NewService(
	database *gorm.DB,
	contents content.Store,
	provider ai.Provider,
	proposals *core.ProposalService,
	workbench *core.WorkbenchService,
	implementation *core.ImplementationService,
) (*Service, error) {
	if database == nil || contents == nil || provider == nil || proposals == nil || workbench == nil || implementation == nil {
		return nil, errors.New("generation dependencies are required")
	}
	return &Service{
		database: database, contents: contents, provider: provider,
		proposals: proposals, workbench: workbench, implementation: implementation,
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
	input, err := s.artifactInput(ctx, manifest)
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	result, err := s.provider.Generate(ctx, ai.Request{
		RunID: manifest.ID, Model: model, Instructions: artifactProposalInstructions(manifest.JobType),
		Input: input, OutputSchema: artifactProposalSchema,
		OutputSchemaName: "artifact_patch_proposal", MaxOutputTokens: 32_768,
	})
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	var output struct {
		Operations  []domain.ProposalOperation `json:"operations"`
		Assumptions []string                   `json:"assumptions"`
		Questions   []string                   `json:"questions"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		return ArtifactGenerationResult{}, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	proposal, err := s.proposals.CreateProposal(ctx, manifest.ProjectID, actorID, core.CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: manifest.BaseRevision.ArtifactID,
		Operations: output.Operations, Assumptions: output.Assumptions, Questions: output.Questions,
	})
	if err != nil {
		return ArtifactGenerationResult{}, err
	}
	return ArtifactGenerationResult{Proposal: proposal, Provider: result.Provider, Model: result.Model, Usage: result.Usage}, nil
}

func (s *Service) GenerateImplementation(ctx context.Context, bundleID, actorID, model, instruction string) (ImplementationGenerationResult, error) {
	bundle, err := s.workbench.GetBundle(ctx, bundleID, actorID)
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	input, err := s.implementationInput(ctx, bundle, instruction)
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	result, err := s.provider.Generate(ctx, ai.Request{
		RunID: bundle.ID, Model: model, Instructions: implementationInstructions,
		Input: input, OutputSchema: implementationProposalSchema,
		OutputSchemaName: "implementation_proposal", MaxOutputTokens: 65_536,
	})
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	var output core.CreateImplementationProposalInput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		return ImplementationGenerationResult{}, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	output.BuildManifestID = bundle.ID
	proposal, err := s.implementation.Create(ctx, bundle.ProjectID, actorID, output)
	if err != nil {
		return ImplementationGenerationResult{}, err
	}
	return ImplementationGenerationResult{Proposal: proposal, Provider: result.Provider, Model: result.Model, Usage: result.Usage}, nil
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

func (s *Service) implementationInput(ctx context.Context, bundle core.WorkbenchBundle, instruction string) (json.RawMessage, error) {
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
	return json.Marshal(map[string]any{
		"applicationBuildManifest": bundle,
		"instruction":              strings.TrimSpace(instruction),
		"sourceContents":           sourceContents,
		"currentWorkspace":         workspace,
	})
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
	if sensitiveKey.MatchString(key) {
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
	return strings.Join([]string{
		"You are an artifact transformation worker in a governed product-development workflow.",
		"The input manifest and all source versions are immutable data. Never claim that you changed canonical data.",
		"Return an RFC 6901 JSON-pointer patch proposal against baseContent.",
		"Preserve human-authored content unless the requested transformation requires a precise change.",
		"Use stable requirement, node, state, layer, and operation IDs. Do not invent server UUIDs.",
		"Every operation must have a unique client operation ID, explicit dependencies, and a concise rationale.",
		"Record uncertainty as assumptions or questions instead of silently guessing.",
		"Job type: " + jobType + ".",
	}, " ")
}

const implementationInstructions = "You are an application implementation worker. Consume only the frozen ApplicationBuildManifest and pinned source contents. Return a reviewable implementation proposal; never claim to have written files. Use safe relative paths and never generate .env, credentials, .git, dependency caches, or build output. For every existing file update/delete/rename, copy its exact contentHash into expectedHash. New files must use an empty expectedHash. Include routes, APIs, migrations, tests, preview expectations, trace links, diagnostics, assumptions, and explicit unimplemented items. Generate tests for acceptance criteria and preserve human workspace changes."

var artifactProposalSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["operations","assumptions","questions"],
  "properties":{
    "operations":{
      "type":"array","minItems":1,"maxItems":5000,
      "items":{
        "type":"object","additionalProperties":false,
        "required":["id","kind","path","value","dependsOn","rationale"],
        "properties":{
          "id":{"type":"string","minLength":1,"maxLength":120},
          "kind":{"type":"string","enum":["add","replace","remove"]},
          "path":{"type":"string","maxLength":2048},
          "value":{},
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
    "routes":{"type":"array","items":{"type":"object"}},
    "apis":{"type":"array","items":{"type":"object"}},
    "migrations":{"type":"array","items":{"type":"object"}},
    "tests":{"type":"array","items":{"type":"object"}},
    "previews":{"type":"array","items":{"type":"object"}},
    "traceLinks":{"type":"array","items":{"type":"object"}},
    "diagnostics":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["code","path","message","severity"],"properties":{"code":{"type":"string"},"path":{"type":"string"},"message":{"type":"string"},"severity":{"type":"string","enum":["info","warning","blocker"]}}}},
    "assumptions":{"type":"array","items":{"type":"string"}},
    "unimplementedItems":{"type":"array","items":{"type":"string"}}
  }
}`)
