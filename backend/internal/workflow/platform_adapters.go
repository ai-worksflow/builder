package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/generation"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type PlatformDependencies struct {
	Store         Store
	CoreProposals CoreProposalAPI
	Generation    interface {
		ArtifactProposalGenerator
		ImplementationGenerator
	}
	Workbench           CoreWorkbenchAPI
	ArtifactInputs      ArtifactInputValidator
	TargetArtifacts     TargetArtifactInitializer
	RequirementBaseline RequirementBaselineCompiler
	WorkbenchCompletion WorkbenchCompletionValidator
	ReviewGate          ReviewGateVerifier
	Access              PublishAuthorizer
	FanOut              FanOutResolver
	Quality             QualityEvaluator
	Publisher           Publisher
	DefaultModel        string
	BuildInstruction    string
	Clock               Clock
	IDs                 IDGenerator
}

// NewPlatformEngine exposes a single bootstrap seam without coupling app.go or
// the HTTP router to concrete runner implementations.
func NewPlatformEngine(dependencies PlatformDependencies) (*Engine, error) {
	if dependencies.Store == nil || dependencies.CoreProposals == nil || dependencies.Generation == nil || dependencies.Workbench == nil || dependencies.ArtifactInputs == nil || dependencies.TargetArtifacts == nil || dependencies.WorkbenchCompletion == nil || dependencies.ReviewGate == nil || dependencies.Access == nil || dependencies.DefaultModel == "" {
		return nil, fmt.Errorf("workflow store, artifact input/target, proposal, generation, workbench and access dependencies are required")
	}
	engine, err := NewEngine(dependencies.Store)
	if err != nil {
		return nil, err
	}
	if dependencies.Clock != nil {
		engine.Clock = dependencies.Clock
	}
	if dependencies.IDs != nil {
		engine.IDs = dependencies.IDs
	}
	engine.ManifestFreezer = CoreManifestFreezer{
		Proposals: dependencies.CoreProposals, Targets: dependencies.TargetArtifacts,
		RequirementBaseline: dependencies.RequirementBaseline,
	}
	engine.ArtifactInputs = dependencies.ArtifactInputs
	engine.WorkbenchCompletion = dependencies.WorkbenchCompletion
	engine.ReviewGate = dependencies.ReviewGate
	engine.ProposalDispatcher = GenerationProposalDispatcher{Generation: dependencies.Generation, DefaultModel: dependencies.DefaultModel}
	engine.BuildManifestHook = CoreWorkbenchManifestHook{Workbench: dependencies.Workbench, Proposals: dependencies.CoreProposals, Now: engine.now}
	engine.ConditionEvaluator = DeclarativeConditionEvaluator{}
	registry := NewMapRegistry()
	_ = registry.Register(domain.NodeFanOut, FanOutRunner{Resolver: DefinitionFanOutResolver{DeliverySlices: dependencies.FanOut}})
	_ = registry.Register(domain.NodeWorkbenchBuild, GenerationWorkbenchRunner{Generation: dependencies.Generation, DefaultModel: dependencies.DefaultModel, Instruction: dependencies.BuildInstruction})
	if dependencies.Quality != nil {
		_ = registry.Register(domain.NodeQualityGate, QualityGateRunner{Evaluator: dependencies.Quality, Access: dependencies.Access})
	}
	if dependencies.Publisher != nil {
		_ = registry.Register(domain.NodePublish, PublishRunner{Publisher: dependencies.Publisher, Access: dependencies.Access})
	}
	engine.Runners = registry
	return engine, nil
}

// CoreArtifactInputValidator enforces the declarative input gate against the
// exact artifact revisions referenced by the run manifest.
type CoreArtifactInputValidator struct{ Database *gorm.DB }

func (v CoreArtifactInputValidator) Validate(ctx context.Context, execution Execution, manifest domain.InputManifest) (json.RawMessage, error) {
	if v.Database == nil || execution.Definition.ArtifactInput == nil {
		return nil, fmt.Errorf("artifact input database and node config are required")
	}
	config := execution.Definition.ArtifactInput
	refs := artifactInputRefs(manifest)
	if len(refs) < config.MinimumArtifacts {
		return nil, fmt.Errorf("artifact input requires at least %d exact revisions", config.MinimumArtifacts)
	}
	accepted := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		var row struct {
			storage.ArtifactRevisionModel
			Kind      string `gorm:"column:artifact_kind"`
			ProjectID string `gorm:"column:artifact_project_id"`
		}
		err := v.Database.WithContext(ctx).Table("artifact_revisions").
			Select("artifact_revisions.*, artifacts.kind AS artifact_kind, artifacts.project_id::text AS artifact_project_id").
			Joins("JOIN artifacts ON artifacts.id = artifact_revisions.artifact_id").
			Where("artifact_revisions.id = ? AND artifact_revisions.artifact_id = ? AND artifact_revisions.content_hash = ?", ref.RevisionID, ref.ArtifactID, ref.ContentHash).
			Take(&row).Error
		if err != nil {
			return nil, err
		}
		artifactType, err := validateArtifactInputRevision(
			*config, manifest.ProjectID, row.ProjectID, row.WorkflowStatus, row.Kind,
		)
		if err != nil {
			return nil, err
		}
		accepted = append(accepted, map[string]any{"ref": ref, "artifactType": artifactType, "kind": row.Kind, "status": row.WorkflowStatus})
	}
	return artifactInputOutput(manifest, refs, accepted)
}

func artifactInputRefs(manifest domain.InputManifest) []domain.ArtifactRef {
	refs := make([]domain.ArtifactRef, 0, len(manifest.Sources)+1)
	seen := map[string]bool{}
	appendRef := func(ref domain.ArtifactRef) {
		key := ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" + ref.AnchorID
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, ref)
	}
	if manifest.BaseRevision != nil {
		appendRef(*manifest.BaseRevision)
	}
	for _, source := range manifest.Sources {
		appendRef(source.Ref)
	}
	return refs
}

func validateArtifactInputRevision(
	config domain.ArtifactInputNodeConfig,
	manifestProjectID, revisionProjectID, workflowStatus, kind string,
) (domain.ArtifactType, error) {
	if revisionProjectID != manifestProjectID {
		return "", fmt.Errorf("artifact input revision is outside the manifest project")
	}
	if config.RequireApproved && workflowStatus != "approved" {
		return "", fmt.Errorf("artifact input revision is not approved")
	}
	artifactType := workflowArtifactType(kind)
	if len(config.AllowedTypes) > 0 {
		allowed := false
		for _, candidate := range config.AllowedTypes {
			if candidate == artifactType {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("artifact kind %s is not allowed by the input node", kind)
		}
	}
	return artifactType, nil
}

func artifactInputOutput(
	manifest domain.InputManifest,
	refs []domain.ArtifactRef,
	accepted []map[string]any,
) (json.RawMessage, error) {
	return domain.CanonicalJSON(map[string]any{
		"artifactRevisions": refs,
		"payload": map[string]any{
			"manifestId": manifest.ID, "manifestHash": manifest.Hash, "artifacts": accepted,
		},
	})
}

// CoreReviewGateVerifier requires every exact upstream artifact revision to
// have an approved canonical review whose policy is at least as strict as the
// workflow node. It deliberately does not mutate review state.
type CoreReviewGateVerifier struct{ Database *gorm.DB }

func (v CoreReviewGateVerifier) VerifyApproval(
	ctx context.Context,
	projectID string,
	refs []domain.ArtifactRef,
	config domain.ReviewGateNodeConfig,
) error {
	if v.Database == nil {
		return fmt.Errorf("review gate database is required")
	}
	if len(refs) == 0 {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "artifactRevisions", Message: "review gate requires exact upstream artifact revisions"}
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return err
		}
		key := ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash
		if seen[key] {
			continue
		}
		seen[key] = true
		var revision storage.ArtifactRevisionModel
		err := v.Database.WithContext(ctx).Table("artifact_revisions").
			Select("artifact_revisions.*").
			Joins("JOIN artifacts ON artifacts.id = artifact_revisions.artifact_id").
			Where("artifact_revisions.id = ? AND artifact_revisions.artifact_id = ? AND artifact_revisions.content_hash = ? AND artifact_revisions.workflow_status = 'approved' AND artifacts.project_id = ?", ref.RevisionID, ref.ArtifactID, ref.ContentHash, projectID).
			Take(&revision).Error
		if err != nil {
			return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "review", Message: "exact upstream revision is not canonically approved"}
		}
		var requests []storage.ReviewRequestModel
		if err := v.Database.WithContext(ctx).
			Where("project_id = ? AND artifact_id = ? AND revision_id = ? AND content_hash = ? AND status = 'approved'", projectID, ref.ArtifactID, ref.RevisionID, ref.ContentHash).
			Order("closed_at DESC").Find(&requests).Error; err != nil {
			return err
		}
		policySatisfied := false
		for _, request := range requests {
			var policy core.ReviewPolicy
			if err := json.Unmarshal(request.Policy, &policy); err != nil {
				return fmt.Errorf("decode canonical review policy: %w", err)
			}
			if policy.MinimumApprovals < config.MinimumApprovals {
				continue
			}
			if config.ProhibitSelfReview && !policy.ProhibitSelfReview {
				continue
			}
			policySatisfied = true
			break
		}
		if !policySatisfied {
			return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "review", Message: "canonical review policy does not satisfy the workflow gate"}
		}
	}
	return nil
}

func workflowArtifactType(kind string) domain.ArtifactType {
	switch kind {
	case "project_brief", "product_requirements", "decision_record", "glossary_policy", "reference_source", "change_request", "requirement_baseline":
		return domain.ArtifactDocument
	case "blueprint", "page_spec", "api_contract", "data_contract", "permission_contract":
		return domain.ArtifactBlueprint
	case "prototype", "prototype_flow", "fixture_bundle", "design_system", "token_set", "component_registry":
		return domain.ArtifactPrototype
	case "workspace":
		return domain.ArtifactImplementation
	case "test_report", "quality_report":
		return domain.ArtifactTest
	default:
		return domain.ArtifactType(kind)
	}
}

type CoreWorkbenchCompletionValidator struct{ Database *gorm.DB }

func (v CoreWorkbenchCompletionValidator) ValidateCompletion(ctx context.Context, execution Execution, output json.RawMessage) (string, error) {
	if v.Database == nil {
		return "", fmt.Errorf("workbench completion database is required")
	}
	var envelope struct {
		ImplementationProposalIDs []string           `json:"implementationProposalIds"`
		WorkspaceRevision         domain.ArtifactRef `json:"workspaceRevision"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		return "", err
	}
	if len(envelope.ImplementationProposalIDs) == 0 {
		return "", fmt.Errorf("workbench completion requires implementation proposals")
	}
	if err := envelope.WorkspaceRevision.Validate(); err != nil {
		return "", err
	}
	buildManifest, err := buildManifestFromExecution(execution)
	if err != nil {
		return "", err
	}
	allowedBundles := map[string]bool{}
	for _, bundleID := range buildManifest.BundleIDs {
		allowedBundles[bundleID] = true
	}
	if len(envelope.ImplementationProposalIDs) != len(allowedBundles) {
		return "", fmt.Errorf("workbench completion requires exactly one applied implementation proposal for every frozen bundle")
	}
	seen, coveredBundles := map[string]bool{}, map[string]bool{}
	for _, proposalID := range envelope.ImplementationProposalIDs {
		parsedProposalID, err := uuid.Parse(proposalID)
		if err != nil {
			return "", fmt.Errorf("implementation proposal id is invalid")
		}
		proposalID = parsedProposalID.String()
		if seen[proposalID] {
			return "", fmt.Errorf("duplicate implementation proposal %s", proposalID)
		}
		seen[proposalID] = true
		var proposal storage.ImplementationProposalModel
		if err := v.Database.WithContext(ctx).Where("id = ? AND project_id = ?", proposalID, execution.Run.ProjectID).Take(&proposal).Error; err != nil {
			return "", err
		}
		bundleID := proposal.BuildManifestID.String()
		if !allowedBundles[bundleID] || coveredBundles[bundleID] || proposal.AppliedAt == nil || (proposal.Status != "applied" && proposal.Status != "partially_applied") {
			return "", fmt.Errorf("implementation proposal %s is not an applied output of the frozen build manifest", proposalID)
		}
		coveredBundles[bundleID] = true
	}
	var workspace storage.ArtifactRevisionModel
	err = v.Database.WithContext(ctx).Table("artifact_revisions").
		Select("artifact_revisions.*").
		Joins("JOIN artifacts ON artifacts.id = artifact_revisions.artifact_id").
		Where("artifact_revisions.id = ? AND artifact_revisions.artifact_id = ? AND artifact_revisions.content_hash = ? AND artifact_revisions.workflow_status = 'approved' AND artifacts.project_id = ? AND artifacts.kind = 'workspace'", envelope.WorkspaceRevision.RevisionID, envelope.WorkspaceRevision.ArtifactID, envelope.WorkspaceRevision.ContentHash, execution.Run.ProjectID).
		Take(&workspace).Error
	if err != nil {
		return "", err
	}
	lineageProposals := map[string]bool{}
	visitedRevisions := map[uuid.UUID]bool{}
	current := workspace
	for current.ID != uuid.Nil {
		if visitedRevisions[current.ID] {
			return "", fmt.Errorf("workspace revision lineage contains a cycle")
		}
		visitedRevisions[current.ID] = true
		if current.ImplementationProposalID != nil {
			lineageProposals[current.ImplementationProposalID.String()] = true
		}
		if current.ParentRevisionID == nil {
			break
		}
		if len(visitedRevisions) > 10_000 {
			return "", fmt.Errorf("workspace revision lineage exceeds the validation limit")
		}
		var parent storage.ArtifactRevisionModel
		if err := v.Database.WithContext(ctx).
			Where("id = ? AND artifact_id = ?", *current.ParentRevisionID, workspace.ArtifactID).
			Take(&parent).Error; err != nil {
			return "", err
		}
		current = parent
	}
	for proposalID := range seen {
		if !lineageProposals[proposalID] {
			return "", fmt.Errorf("workspace revision is not descended from implementation proposal %s", proposalID)
		}
	}
	return workspace.ID.String(), nil
}

// CoreContentStoreAdapter lets workflow persistence share the finalized Mongo
// payloads written by core services rather than duplicating proposal/manifest data.
type CoreContentStoreAdapter struct{ Store content.Store }

func (a CoreContentStoreAdapter) Put(ctx context.Context, namespace, id string, payload []byte) (string, string, string, error) {
	if a.Store == nil {
		return "", "", "", fmt.Errorf("core content store is required")
	}
	var envelope struct {
		ProjectID string `json:"projectId"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.ProjectID == "" {
		return "", "", "", fmt.Errorf("workflow content must contain projectId")
	}
	reference, err := a.Store.PutPending(ctx, envelope.ProjectID, namespace, id, 1, json.RawMessage(payload))
	if err != nil {
		return "", "", "", err
	}
	if err := a.Store.Finalize(ctx, reference.ID); err != nil {
		_ = a.Store.Abort(context.Background(), reference.ID)
		return "", "", "", err
	}
	return "mongo", reference.ID, reference.ContentHash, nil
}

func (a CoreContentStoreAdapter) Get(ctx context.Context, store, ref, expectedHash string) ([]byte, error) {
	if a.Store == nil {
		return nil, fmt.Errorf("core content store is required")
	}
	if store != "mongo" {
		return nil, fmt.Errorf("unsupported platform content store %q", store)
	}
	stored, err := a.Store.Get(ctx, ref, expectedHash)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), stored.Payload...), nil
}

type CoreProposalAPI interface {
	CreateManifest(context.Context, string, string, core.CreateManifestInput) (domain.InputManifest, error)
	GetManifest(context.Context, string, string) (domain.InputManifest, error)
}

type TargetArtifactInitializer interface {
	EnsureTarget(context.Context, Execution, string, []core.ManifestSourceInput) (*core.VersionRef, error)
}

type CoreArtifactAPI interface {
	Create(context.Context, string, string, core.CreateArtifactInput) (core.VersionedArtifact, error)
	List(context.Context, string, string, string, string) ([]core.Artifact, error)
	Get(context.Context, string, string, bool) (core.VersionedArtifact, error)
	CreateRevision(context.Context, string, string, string, core.CreateRevisionInput) (core.ArtifactRevision, error)
}

type CoreTargetArtifactInitializer struct{ Artifacts CoreArtifactAPI }

func (i CoreTargetArtifactInitializer) EnsureTarget(
	ctx context.Context,
	execution Execution,
	jobType string,
	sources []core.ManifestSourceInput,
) (*core.VersionRef, error) {
	if i.Artifacts == nil {
		return nil, fmt.Errorf("artifact service is required")
	}
	kind, key, title, content, ok := targetArtifactTemplate(execution, jobType)
	if !ok {
		return nil, nil
	}
	input, err := i.targetArtifactInput(ctx, execution, kind, key, title, content, sources)
	if err != nil {
		return nil, err
	}
	artifacts, err := i.Artifacts.List(ctx, execution.Run.ProjectID, execution.Run.StartedBy, kind, "")
	if err != nil {
		return nil, err
	}
	var selected *core.Artifact
	for index := range artifacts {
		if artifacts[index].ArtifactKey == key {
			selected = &artifacts[index]
			break
		}
	}
	if selected == nil {
		created, err := i.Artifacts.Create(ctx, execution.Run.ProjectID, execution.Run.StartedBy, input)
		if err != nil {
			return nil, err
		}
		selected = &created.Artifact
	}
	versioned, err := i.Artifacts.Get(ctx, selected.ID, execution.Run.StartedBy, true)
	if err != nil {
		return nil, err
	}
	if err := validateExistingTargetLineage(kind, versioned, input); err != nil {
		return nil, err
	}
	if versioned.LatestRevision == nil {
		if versioned.Draft == nil {
			return nil, fmt.Errorf("target artifact has neither a draft nor a revision")
		}
		revision, err := i.Artifacts.CreateRevision(ctx, selected.ID, execution.Run.StartedBy, versioned.Draft.ETag, core.CreateRevisionInput{
			ChangeSummary: "Initialize target for workflow AI proposal", ChangeSource: "system",
		})
		if err != nil {
			return nil, err
		}
		return &core.VersionRef{ArtifactID: selected.ID, RevisionID: revision.ID, ContentHash: revision.ContentHash}, nil
	}
	return &core.VersionRef{ArtifactID: selected.ID, RevisionID: versioned.LatestRevision.ID, ContentHash: versioned.LatestRevision.ContentHash}, nil
}

func (i CoreTargetArtifactInitializer) targetArtifactInput(
	ctx context.Context,
	execution Execution,
	kind string,
	key string,
	title string,
	content json.RawMessage,
	sources []core.ManifestSourceInput,
) (core.CreateArtifactInput, error) {
	input := core.CreateArtifactInput{
		Kind: kind, ArtifactKey: key, Title: title, SchemaVersion: 1, Content: content,
	}
	switch kind {
	case "blueprint":
		source, err := i.uniqueSourceByKind(ctx, execution, sources, "requirement_baseline")
		if err != nil {
			return core.CreateArtifactInput{}, fmt.Errorf("Blueprint target lineage: %w", err)
		}
		if source.Ref.AnchorID != nil {
			return core.CreateArtifactInput{}, fmt.Errorf("Blueprint target requires a whole Requirement Baseline revision")
		}
		input.SourceVersions = []core.ArtifactSourceInput{{
			Ref: source.Ref, Purpose: "requirement_baseline", Required: true,
		}}
	case "page_spec":
		source, err := i.uniqueSourceByKind(ctx, execution, sources, "blueprint")
		if err != nil {
			return core.CreateArtifactInput{}, fmt.Errorf("PageSpec target lineage: %w", err)
		}
		if source.Ref.AnchorID == nil || strings.TrimSpace(*source.Ref.AnchorID) == "" {
			return core.CreateArtifactInput{}, fmt.Errorf("PageSpec target requires an anchored Blueprint Page revision")
		}
		pageNodeID := strings.TrimSpace(*source.Ref.AnchorID)
		input.Content, err = pageSpecTargetContent(execution, pageNodeID, title)
		if err != nil {
			return core.CreateArtifactInput{}, err
		}
		input.SourceVersions = []core.ArtifactSourceInput{{
			Ref: source.Ref, Purpose: "blueprint", Required: true,
		}}
	case "prototype":
		source, err := i.uniqueSourceByKind(ctx, execution, sources, "page_spec")
		if err != nil {
			return core.CreateArtifactInput{}, fmt.Errorf("Prototype target lineage: %w", err)
		}
		if source.Ref.AnchorID != nil {
			return core.CreateArtifactInput{}, fmt.Errorf("Prototype target requires a whole PageSpec revision")
		}
		input.Content, err = prototypeTargetContent(execution, source.Ref)
		if err != nil {
			return core.CreateArtifactInput{}, err
		}
		input.SourceVersions = []core.ArtifactSourceInput{{
			Ref: source.Ref, Purpose: "page_spec", Required: true,
		}}
	}
	return input, nil
}

func (i CoreTargetArtifactInitializer) uniqueSourceByKind(
	ctx context.Context,
	execution Execution,
	sources []core.ManifestSourceInput,
	kind string,
) (core.ManifestSourceInput, error) {
	matches := make([]core.ManifestSourceInput, 0, 1)
	for _, source := range sources {
		if err := fromCoreVersionRef(source.Ref).Validate(); err != nil {
			return core.ManifestSourceInput{}, err
		}
		artifact, err := i.Artifacts.Get(ctx, source.Ref.ArtifactID, execution.Run.StartedBy, false)
		if err != nil {
			return core.ManifestSourceInput{}, err
		}
		if artifact.Artifact.ProjectID != execution.Run.ProjectID {
			return core.ManifestSourceInput{}, fmt.Errorf("source artifact belongs to another project")
		}
		if artifact.Artifact.Kind == kind {
			matches = append(matches, source)
		}
	}
	if len(matches) != 1 {
		return core.ManifestSourceInput{}, fmt.Errorf("requires exactly one exact %s source, got %d", kind, len(matches))
	}
	return matches[0], nil
}

func validateExistingTargetLineage(
	kind string,
	artifact core.VersionedArtifact,
	desired core.CreateArtifactInput,
) error {
	if kind != "blueprint" && kind != "page_spec" && kind != "prototype" {
		return nil
	}
	var content json.RawMessage
	var sources []core.ArtifactSource
	if artifact.Draft != nil {
		content = artifact.Draft.Content
		sources = artifact.Draft.SourceVersions
	} else if artifact.LatestRevision != nil {
		content = artifact.LatestRevision.Content
		sources = artifact.LatestRevision.SourceVersions
	} else {
		return fmt.Errorf("existing %s target has no exact draft or revision lineage", kind)
	}
	if len(sources) != len(desired.SourceVersions) {
		return fmt.Errorf("existing %s target lineage differs from the immutable workflow input", kind)
	}
	for index, expected := range desired.SourceVersions {
		actual := sources[index]
		if actual.Purpose != expected.Purpose || actual.Required != expected.Required ||
			!sameCoreVersionRef(actual.VersionRef, expected.Ref) {
			return fmt.Errorf("existing %s target source %d differs from the immutable workflow input", kind, index)
		}
	}
	switch kind {
	case "page_spec":
		var actual, expected struct {
			BlueprintPageNodeID string `json:"blueprintPageNodeId"`
		}
		if json.Unmarshal(content, &actual) != nil || json.Unmarshal(desired.Content, &expected) != nil ||
			strings.TrimSpace(actual.BlueprintPageNodeID) != strings.TrimSpace(expected.BlueprintPageNodeID) {
			return fmt.Errorf("existing PageSpec target content differs from its exact Blueprint Page anchor")
		}
	case "prototype":
		var actual, expected struct {
			PageSpecRevision core.VersionRef `json:"pageSpecRevision"`
			Exploratory      bool            `json:"exploratory"`
		}
		if json.Unmarshal(content, &actual) != nil || json.Unmarshal(desired.Content, &expected) != nil ||
			actual.Exploratory != expected.Exploratory || !sameCoreVersionRef(actual.PageSpecRevision, expected.PageSpecRevision) {
			return fmt.Errorf("existing Prototype target content differs from its exact PageSpec revision")
		}
	}
	return nil
}

func sameCoreVersionRef(left, right core.VersionRef) bool {
	leftAnchor, rightAnchor := "", ""
	if left.AnchorID != nil {
		leftAnchor = strings.TrimSpace(*left.AnchorID)
	}
	if right.AnchorID != nil {
		rightAnchor = strings.TrimSpace(*right.AnchorID)
	}
	return left.ArtifactID == right.ArtifactID && left.RevisionID == right.RevisionID &&
		left.ContentHash == right.ContentHash && leftAnchor == rightAnchor
}

func pageSpecTargetContent(execution Execution, pageNodeID, title string) (json.RawMessage, error) {
	slice, ok := targetExecutionSlice(execution)
	if !ok {
		return nil, fmt.Errorf("PageSpec target requires one exact workflow slice")
	}
	route := "/" + strings.ToLower(strings.Trim(strings.ReplaceAll(slice.Key, ".", "-"), "-/"))
	userGoal := "Complete " + strings.TrimSpace(title)
	var payload map[string]any
	if len(slice.Payload) > 0 && json.Unmarshal(slice.Payload, &payload) == nil {
		if value, ok := payload["route"].(string); ok && strings.HasPrefix(strings.TrimSpace(value), "/") {
			route = strings.TrimSpace(value)
		}
		if value, ok := payload["userGoal"].(string); ok && strings.TrimSpace(value) != "" {
			userGoal = strings.TrimSpace(value)
		}
	}
	return domain.CanonicalJSON(map[string]any{
		"schemaVersion":       1,
		"blueprintPageNodeId": pageNodeID,
		"title":               title,
		"route":               route,
		"userGoal":            userGoal,
		"states":              []any{},
		"dataBindings":        []any{},
		"interactions":        []any{},
	})
}

func prototypeTargetContent(execution Execution, pageSpec core.VersionRef) (json.RawMessage, error) {
	slice, ok := targetExecutionSlice(execution)
	if !ok {
		return nil, fmt.Errorf("Prototype target requires one exact workflow slice")
	}
	exploratory := false
	var payload map[string]any
	if len(slice.Payload) > 0 && json.Unmarshal(slice.Payload, &payload) == nil {
		exploratory, _ = payload["exploratory"].(bool)
	}
	return domain.CanonicalJSON(map[string]any{
		"schemaVersion":    1,
		"pageSpecRevision": pageSpec,
		"exploratory":      exploratory,
		"states":           []any{},
		"breakpoints":      []any{},
		"layers":           map[string]any{},
		"frames":           []any{},
		"interactions":     []any{},
		"fixtures":         []any{},
	})
}

func targetExecutionSlice(execution Execution) (SliceContext, bool) {
	sliceID := strings.TrimSpace(execution.Node.SliceID)
	if sliceID == "" {
		sliceID = strings.TrimSpace(execution.Run.Context.Nodes[execution.Node.Key].SliceID)
	}
	slice, ok := execution.Run.Context.Slices[sliceID]
	return slice, ok && sliceID != ""
}

func targetArtifactTemplate(execution Execution, jobType string) (kind, key, title string, content json.RawMessage, ok bool) {
	switch jobType {
	case "derive_requirements":
		return "product_requirements", "DOC-REQUIREMENTS", "Product Requirements", json.RawMessage(`{"schemaVersion":1,"kind":"productRequirements","blocks":[]}`), true
	case "decompose_pages":
		return "blueprint", "BLUEPRINT-MAIN", "Product Blueprint", json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[],"pageSpecs":[]}`), true
	case "generate_page_spec":
		slice, exists := targetExecutionSlice(execution)
		if !exists {
			return "page_spec", "PAGE-SPEC-MISSING-SLICE", "PageSpec", json.RawMessage(`{}`), true
		}
		suffix := targetSliceSuffix(slice, execution)
		return "page_spec", "PAGE-SPEC-" + suffix, "PageSpec · " + slice.Title, json.RawMessage(`{}`), true
	case "generate_prototype":
		slice, _ := targetExecutionSlice(execution)
		suffix := targetSliceSuffix(slice, execution)
		return "prototype", "PROTOTYPE-" + suffix, "Prototype · " + slice.Title, json.RawMessage(`{"schemaVersion":1,"states":[],"breakpoints":[],"layers":[],"frames":[],"interactions":[],"fixtures":[]}`), true
	default:
		return "", "", "", nil, false
	}
}

func targetSliceSuffix(slice SliceContext, execution Execution) string {
	suffix := strings.ToUpper(nonAlphaNumeric.ReplaceAllString(slice.Key, "-"))
	suffix = strings.Trim(suffix, "-")
	if suffix == "" {
		sliceID := execution.Node.SliceID
		if sliceID == "" {
			sliceID = execution.Run.Context.Nodes[execution.Node.Key].SliceID
		}
		suffix = strings.ToUpper(strings.ReplaceAll(sliceID, "-", ""))
		if len(suffix) > 12 {
			suffix = suffix[:12]
		}
	}
	if suffix == "" {
		suffix = "MISSING-SLICE"
	}
	return suffix
}

var nonAlphaNumeric = regexp.MustCompile(`[^A-Za-z0-9]+`)

// CoreManifestFreezer creates a new immutable, authorization-checked manifest
// from the exact revisions pinned by the run's prior manifest.
type CoreManifestFreezer struct {
	Proposals           CoreProposalAPI
	Targets             TargetArtifactInitializer
	RequirementBaseline RequirementBaselineCompiler
}

type RequirementBaselineCompiler interface {
	Compile(context.Context, string, string, []core.VersionRef) (core.ArtifactRevision, error)
}

func (a CoreManifestFreezer) Freeze(ctx context.Context, execution Execution) (domain.InputManifest, error) {
	if a.Proposals == nil || execution.Run.InputManifest == nil {
		return domain.InputManifest{}, fmt.Errorf("proposal service and run input manifest are required")
	}
	upstream, err := a.Proposals.GetManifest(ctx, execution.Run.InputManifest.ID, execution.Run.StartedBy)
	if err != nil {
		return domain.InputManifest{}, err
	}
	if upstream.Ref() != *execution.Run.InputManifest {
		return domain.InputManifest{}, fmt.Errorf("run input manifest hash changed")
	}
	sourceByArtifact := map[string]core.ManifestSourceInput{}
	for _, source := range upstream.Sources {
		if err := source.Ref.Validate(); err != nil {
			return domain.InputManifest{}, err
		}
		sourceByArtifact[source.Ref.ArtifactID] = core.ManifestSourceInput{Ref: toCoreVersionRef(source.Ref), Purpose: source.Purpose}
	}
	if upstream.BaseRevision != nil {
		sourceByArtifact[upstream.BaseRevision.ArtifactID] = core.ManifestSourceInput{Ref: toCoreVersionRef(*upstream.BaseRevision), Purpose: "workflow_input"}
	}
	jobType, outputSchema := string(execution.Definition.Type), "workflow-proposal/v1"
	if execution.Definition.AITransform != nil {
		jobType = execution.Definition.AITransform.JobType
		outputSchema = execution.Definition.AITransform.OutputSchemaVersion
	}
	if execution.Definition.AI != nil {
		jobType = execution.Definition.AI.JobType
		outputSchema = execution.Definition.AI.OutputSchemaVersion
	}
	deliverySliceID := ""
	currentSliceID := execution.Node.SliceID
	if currentSliceID == "" {
		currentSliceID = execution.Run.Context.Nodes[execution.Node.Key].SliceID
	}
	if currentSliceID != "" {
		deliverySliceID = currentSliceID
	}
	for _, binding := range execution.Inputs.Bindings() {
		for _, ref := range binding.Source.ArtifactRevisions {
			sourceByArtifact[ref.ArtifactID] = core.ManifestSourceInput{Ref: toCoreVersionRef(ref), Purpose: "workflow_node:" + binding.Source.DefinitionNodeID}
		}
	}
	if currentSliceID != "" {
		if slice, exists := execution.Run.Context.Slices[currentSliceID]; exists {
			for purpose, ref := range map[string]*domain.ArtifactRef{"delivery_slice_blueprint": &slice.Blueprint, "delivery_slice_page_spec": slice.PageSpec, "delivery_slice_prototype": slice.Prototype} {
				if ref != nil && ref.Validate() == nil {
					sourceByArtifact[ref.ArtifactID] = core.ManifestSourceInput{Ref: toCoreVersionRef(*ref), Purpose: purpose}
				}
			}
		}
	}
	artifactIDs := make([]string, 0, len(sourceByArtifact))
	for artifactID := range sourceByArtifact {
		artifactIDs = append(artifactIDs, artifactID)
	}
	sort.Strings(artifactIDs)
	sources := make([]core.ManifestSourceInput, 0, len(artifactIDs))
	for _, artifactID := range artifactIDs {
		sources = append(sources, sourceByArtifact[artifactID])
	}
	if jobType == "decompose_pages" {
		if a.RequirementBaseline == nil {
			return domain.InputManifest{}, fmt.Errorf("decompose_pages requires a deterministic Requirement Baseline compiler")
		}
		baselineSources := make([]core.VersionRef, 0, len(sources))
		for _, source := range sources {
			if upstream.BaseRevision != nil && source.Ref.ArtifactID == upstream.BaseRevision.ArtifactID {
				continue
			}
			baselineSources = append(baselineSources, source.Ref)
		}
		baseline, err := a.RequirementBaseline.Compile(
			ctx, execution.Run.ProjectID, execution.Run.StartedBy, baselineSources,
		)
		if err != nil {
			return domain.InputManifest{}, fmt.Errorf("compile Requirement Baseline: %w", err)
		}
		sources = []core.ManifestSourceInput{{
			Ref: core.VersionRef{
				ArtifactID: baseline.ArtifactID, RevisionID: baseline.ID,
				ContentHash: baseline.ContentHash,
			},
			Purpose: "requirement_baseline",
		}}
	}
	var base *core.VersionRef
	if a.Targets != nil {
		base, err = a.Targets.EnsureTarget(ctx, execution, jobType, sources)
		if err != nil {
			return domain.InputManifest{}, err
		}
	}
	if base == nil && upstream.BaseRevision != nil {
		converted := toCoreVersionRef(*upstream.BaseRevision)
		base = &converted
	}
	if base != nil {
		filtered := sources[:0]
		for _, source := range sources {
			if source.Ref.ArtifactID != base.ArtifactID {
				filtered = append(filtered, source)
			}
		}
		sources = filtered
	}
	constraints, err := domain.CanonicalJSON(map[string]any{"workflowRunId": execution.Run.ID, "workflowDefinition": execution.Run.Definition, "nodeId": execution.Definition.ID, "scope": json.RawMessage(execution.Run.Scope), "upstreamManifest": upstream.Ref(), "upstreamConstraints": json.RawMessage(upstream.Constraints)})
	if err != nil {
		return domain.InputManifest{}, err
	}
	return a.Proposals.CreateManifest(ctx, execution.Run.ProjectID, execution.Run.StartedBy, core.CreateManifestInput{JobType: jobType, DeliverySliceID: deliverySliceID, BaseRevision: base, Sources: sources, Constraints: constraints, OutputSchemaVersion: outputSchema})
}

type ArtifactProposalGenerator interface {
	GenerateArtifactProposal(context.Context, string, string, string) (generation.ArtifactGenerationResult, error)
}

type GenerationProposalDispatcher struct {
	Generation   ArtifactProposalGenerator
	DefaultModel string
	ModelFor     func(Execution) string
}

func (d GenerationProposalDispatcher) Dispatch(ctx context.Context, execution Execution, manifest domain.InputManifest) (*domain.ProposalRef, error) {
	if d.Generation == nil {
		return nil, fmt.Errorf("generation service is required")
	}
	if manifest.Ref().ID == "" || manifest.ProjectID != execution.Run.ProjectID {
		return nil, domain.ErrManifestUnpinned
	}
	model := d.DefaultModel
	if d.ModelFor != nil {
		model = d.ModelFor(execution)
	}
	if model == "" {
		return nil, fmt.Errorf("generation model is required")
	}
	result, err := d.Generation.GenerateArtifactProposal(ctx, manifest.ID, execution.Run.StartedBy, model)
	if err != nil {
		return nil, err
	}
	if result.Proposal.Manifest != manifest.Ref() {
		return nil, fmt.Errorf("generated proposal is not pinned to dispatched manifest")
	}
	return &domain.ProposalRef{ID: result.Proposal.ID, PayloadHash: result.Proposal.PayloadHash}, nil
}

type CoreWorkbenchAPI interface {
	CreateBundle(context.Context, string, string, core.CreateWorkbenchBundleInput) (core.WorkbenchBundle, error)
}

type CoreWorkbenchManifestHook struct {
	Workbench CoreWorkbenchAPI
	Proposals CoreProposalAPI
	Now       func() time.Time
}

func (h CoreWorkbenchManifestHook) Compile(ctx context.Context, execution Execution) (BuildManifest, error) {
	if h.Workbench == nil {
		return BuildManifest{}, fmt.Errorf("workbench service is required")
	}
	if execution.Definition.ManifestCompiler == nil {
		return BuildManifest{}, fmt.Errorf("manifest compiler node config is required")
	}
	sliceRefs := execution.Inputs.SliceRefs()
	slices := make([]SliceContext, 0, len(sliceRefs))
	for _, ref := range sliceRefs {
		current, exists := execution.Run.Context.Slices[ref.ID]
		if !exists || current.ID != ref.ID || current.Key != ref.Key || current.FanOutNodeID != ref.FanOutNodeID {
			return BuildManifest{}, fmt.Errorf("incoming delivery slice lineage %q is stale", ref.ID)
		}
		if ref.Blueprint == nil || ref.Blueprint.Validate() != nil {
			return BuildManifest{}, fmt.Errorf("incoming delivery slice lineage %q has no exact Blueprint ref", ref.ID)
		}
		slice := SliceContext{ID: ref.ID, Key: ref.Key, Title: current.Title, FanOutNodeID: ref.FanOutNodeID, Payload: current.Payload, Blueprint: *ref.Blueprint, OwnerID: current.OwnerID}
		if ref.PageSpec != nil {
			value := *ref.PageSpec
			slice.PageSpec = &value
		}
		if ref.Prototype != nil {
			value := *ref.Prototype
			slice.Prototype = &value
		}
		slices = append(slices, slice)
	}
	sort.Slice(slices, func(i, j int) bool { return slices[i].Key < slices[j].Key })
	if len(slices) == 0 {
		return BuildManifest{}, fmt.Errorf("build manifest requires delivery slices")
	}
	bundleIDs := make([]string, 0, len(slices))
	sliceIDs := make([]string, 0, len(slices))
	sources := make([]domain.ArtifactRef, 0)
	for _, slice := range slices {
		if slice.Prototype == nil {
			return BuildManifest{}, fmt.Errorf("slice %s has no exact prototype revision", slice.Key)
		}
		if err := slice.Prototype.Validate(); err != nil {
			return BuildManifest{}, err
		}
		runID, sliceID := execution.Run.ID, slice.ID
		bundle, err := h.Workbench.CreateBundle(ctx, execution.Run.ProjectID, execution.Run.StartedBy, core.CreateWorkbenchBundleInput{PrototypeRevision: toCoreVersionRef(*slice.Prototype), WorkflowRunID: &runID, DeliverySliceID: &sliceID})
		if err != nil {
			return BuildManifest{}, err
		}
		bundleIDs = append(bundleIDs, bundle.ID)
		sliceIDs = append(sliceIDs, slice.ID)
		for _, ref := range refsFromBundle(bundle) {
			sources = appendUniqueArtifactRef(sources, ref)
		}
	}
	if h.Proposals != nil && execution.Run.InputManifest != nil {
		upstream, err := h.Proposals.GetManifest(ctx, execution.Run.InputManifest.ID, execution.Run.StartedBy)
		if err != nil {
			return BuildManifest{}, err
		}
		for _, source := range upstream.Sources {
			sources = appendUniqueArtifactRef(sources, source.Ref)
		}
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	constraints, _ := domain.CanonicalJSON(map[string]any{"definition": execution.Run.Definition, "scope": json.RawMessage(execution.Run.Scope)})
	return BuildManifest{SchemaVersion: execution.Definition.ManifestCompiler.SchemaVersion, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID, SliceIDs: sliceIDs, BundleIDs: bundleIDs, Sources: sources, Constraints: constraints, CreatedAt: now}, nil
}

type ImplementationGenerator interface {
	GenerateImplementation(context.Context, string, string, string, string) (generation.ImplementationGenerationResult, error)
}

type GenerationWorkbenchRunner struct {
	Generation   ImplementationGenerator
	DefaultModel string
	Instruction  string
}

func (r GenerationWorkbenchRunner) Run(ctx context.Context, execution Execution) (WorkerResult, error) {
	if r.Generation == nil {
		return WorkerResult{}, fmt.Errorf("generation service is required")
	}
	manifest, err := buildManifestFromExecution(execution)
	if err != nil {
		return WorkerResult{}, err
	}
	if len(manifest.BundleIDs) == 0 {
		return WorkerResult{}, fmt.Errorf("build manifest has no workbench bundles")
	}
	results := make([]map[string]any, 0, len(manifest.BundleIDs))
	instruction, err := workbenchInstruction(execution.Run.Scope, r.Instruction)
	if err != nil {
		return WorkerResult{}, err
	}
	for _, bundleID := range manifest.BundleIDs {
		generated, err := r.Generation.GenerateImplementation(ctx, bundleID, execution.Run.StartedBy, r.DefaultModel, instruction)
		if err != nil {
			return WorkerResult{}, err
		}
		if generated.Proposal.Status == "applied" {
			return WorkerResult{}, fmt.Errorf("generation returned an already-applied proposal")
		}
		results = append(results, map[string]any{"bundleId": bundleID, "proposalId": generated.Proposal.ID, "payloadHash": generated.Proposal.PayloadHash})
	}
	return WorkerResult{Disposition: ResultWaitInput, Output: mustJSON(map[string]any{"implementationProposals": results})}, nil
}

func workbenchInstruction(scope json.RawMessage, fallback string) (string, error) {
	if len(scope) == 0 {
		return strings.TrimSpace(fallback), nil
	}
	var envelope struct {
		ConversationIntent struct {
			WorkbenchInstruction json.RawMessage `json:"workbenchInstruction"`
		} `json:"conversationIntent"`
	}
	if err := json.Unmarshal(scope, &envelope); err != nil {
		return "", fmt.Errorf("decode workbench instruction from run scope: %w", err)
	}
	if len(envelope.ConversationIntent.WorkbenchInstruction) == 0 || string(envelope.ConversationIntent.WorkbenchInstruction) == "null" {
		return strings.TrimSpace(fallback), nil
	}
	var instruction struct {
		Objective   string   `json:"objective"`
		Constraints []string `json:"constraints,omitempty"`
	}
	if err := json.Unmarshal(envelope.ConversationIntent.WorkbenchInstruction, &instruction); err != nil {
		return "", fmt.Errorf("decode structured workbench instruction: %w", err)
	}
	if strings.TrimSpace(instruction.Objective) == "" {
		return "", fmt.Errorf("conversation workbench instruction objective is required")
	}
	canonical, err := domain.CanonicalJSON(envelope.ConversationIntent.WorkbenchInstruction)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

type FanOutResolver interface {
	Resolve(context.Context, Execution) ([]FanOutItem, error)
}
type FanOutResolverFunc func(context.Context, Execution) ([]FanOutItem, error)

func (f FanOutResolverFunc) Resolve(ctx context.Context, e Execution) ([]FanOutItem, error) {
	return f(ctx, e)
}

type DefinitionFanOutResolver struct {
	DeliverySlices FanOutResolver
}

func (r DefinitionFanOutResolver) Resolve(ctx context.Context, execution Execution) ([]FanOutItem, error) {
	config := execution.Definition.FanOut
	if config == nil {
		return nil, fmt.Errorf("fan-out node config is required")
	}
	if config.ItemKind == "delivery_slice" && r.DeliverySlices != nil {
		return r.DeliverySlices.Resolve(ctx, execution)
	}
	return InputEnvelopeFanOutResolver{}.Resolve(ctx, execution)
}

type InputEnvelopeFanOutResolver struct{}

func (InputEnvelopeFanOutResolver) Resolve(_ context.Context, execution Execution) ([]FanOutItem, error) {
	config := execution.Definition.FanOut
	if config == nil {
		return nil, fmt.Errorf("fan-out node config is required")
	}
	resolvedItems := make([]any, 0)
	matched := false
	for _, binding := range execution.Inputs.Bindings() {
		var value any
		if err := json.Unmarshal(binding.Value, &value); err != nil {
			return nil, fmt.Errorf("decode fan-out input on edge %s: %w", binding.EdgeID, err)
		}
		resolved, found, err := resolveJSONPointer(value, config.ItemsPath)
		if err != nil {
			return nil, fmt.Errorf("fan-out itemsPath %s on edge %s: %w", config.ItemsPath, binding.EdgeID, err)
		}
		if !found {
			continue
		}
		items, ok := resolved.([]any)
		if !ok {
			return nil, fmt.Errorf("fan-out itemsPath %s on edge %s must resolve to an array", config.ItemsPath, binding.EdgeID)
		}
		matched = true
		resolvedItems = append(resolvedItems, items...)
	}
	if !matched {
		return nil, fmt.Errorf("fan-out itemsPath %s did not resolve in any immutable input binding", config.ItemsPath)
	}
	items := make([]FanOutItem, 0, len(resolvedItems))
	for index, value := range resolvedItems {
		keyValue, found, err := resolveJSONPointer(value, config.SliceKeyPath)
		if err != nil {
			return nil, fmt.Errorf("fan-out sliceKeyPath %s for item %d: %w", config.SliceKeyPath, index, err)
		}
		key, ok := keyValue.(string)
		if !found || !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("fan-out sliceKeyPath %s for item %d must resolve to a non-empty string", config.SliceKeyPath, index)
		}
		payload, err := domain.CanonicalJSON(value)
		if err != nil {
			return nil, fmt.Errorf("canonicalize fan-out item %d: %w", index, err)
		}
		title := strings.TrimSpace(key)
		if object, ok := value.(map[string]any); ok {
			if candidate, ok := object["title"].(string); ok && strings.TrimSpace(candidate) != "" {
				title = strings.TrimSpace(candidate)
			}
		}
		item := FanOutItem{Key: strings.TrimSpace(key), Title: title, Payload: payload}
		if config.ItemKind == "delivery_slice" {
			var delivery struct {
				Blueprint domain.ArtifactRef  `json:"blueprint"`
				PageSpec  *domain.ArtifactRef `json:"pageSpec"`
				Prototype *domain.ArtifactRef `json:"prototype"`
				OwnerID   string              `json:"ownerId"`
			}
			if err := json.Unmarshal(payload, &delivery); err != nil {
				return nil, fmt.Errorf("decode delivery slice item %d: %w", index, err)
			}
			if err := delivery.Blueprint.Validate(); err != nil {
				return nil, fmt.Errorf("delivery slice %s blueprint ref: %w", item.Key, err)
			}
			if delivery.PageSpec == nil {
				return nil, fmt.Errorf("delivery slice %s requires an exact PageSpec ref", item.Key)
			}
			if err := delivery.PageSpec.Validate(); err != nil {
				return nil, fmt.Errorf("delivery slice %s PageSpec ref: %w", item.Key, err)
			}
			if delivery.Prototype != nil {
				if err := delivery.Prototype.Validate(); err != nil {
					return nil, fmt.Errorf("delivery slice %s prototype ref: %w", item.Key, err)
				}
			}
			item.Blueprint = delivery.Blueprint
			item.PageSpec = delivery.PageSpec
			item.Prototype = delivery.Prototype
			item.OwnerID = strings.TrimSpace(delivery.OwnerID)
		}
		items = append(items, item)
	}
	return items, nil
}

type ContextFanOutResolver struct{ ValueKey string }

func (r ContextFanOutResolver) Resolve(_ context.Context, execution Execution) ([]FanOutItem, error) {
	key := r.ValueKey
	if key == "" {
		key = "deliverySlices"
	}
	raw := execution.Run.Context.Values[key]
	if len(raw) == 0 {
		return nil, fmt.Errorf("run context has no %s fan-out input", key)
	}
	var items []FanOutItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	for index := range items {
		item := &items[index]
		if err := item.Blueprint.Validate(); err != nil {
			return nil, err
		}
		if item.PageSpec == nil {
			return nil, fmt.Errorf("delivery slice %s requires an exact PageSpec ref", item.Key)
		}
		if err := item.PageSpec.Validate(); err != nil {
			return nil, err
		}
		if item.Prototype != nil {
			if err := item.Prototype.Validate(); err != nil {
				return nil, err
			}
		}
		if len(item.Payload) == 0 {
			item.Payload, _ = domain.CanonicalJSON(item)
		}
	}
	return items, nil
}

type FanOutRunner struct{ Resolver FanOutResolver }

func (r FanOutRunner) Run(ctx context.Context, e Execution) (WorkerResult, error) {
	if r.Resolver == nil {
		return WorkerResult{}, fmt.Errorf("fan-out resolver is required")
	}
	items, err := r.Resolver.Resolve(ctx, e)
	if err != nil {
		return WorkerResult{}, err
	}
	return WorkerResult{Disposition: ResultComplete, FanOutItems: items}, nil
}

type QualityResult struct {
	Passed            bool                `json:"passed"`
	Findings          json.RawMessage     `json:"findings,omitempty"`
	QualityRunID      string              `json:"qualityRunId,omitempty"`
	WorkspaceRevision *domain.ArtifactRef `json:"workspaceRevision,omitempty"`
	BuildManifest     *BuildManifest      `json:"buildManifest,omitempty"`
}
type QualityEvaluator interface {
	Evaluate(context.Context, Execution) (QualityResult, error)
}
type QualityEvaluatorFunc func(context.Context, Execution) (QualityResult, error)

func (f QualityEvaluatorFunc) Evaluate(ctx context.Context, e Execution) (QualityResult, error) {
	return f(ctx, e)
}

type QualityGateRunner struct {
	Evaluator QualityEvaluator
	Access    PublishAuthorizer
}

func (r QualityGateRunner) Run(ctx context.Context, e Execution) (WorkerResult, error) {
	if r.Evaluator == nil || r.Access == nil {
		return WorkerResult{}, fmt.Errorf("quality evaluator and access control are required")
	}
	actor, err := e.ExecutionActor()
	if err != nil {
		return WorkerResult{}, err
	}
	currentRole, err := r.Access.Authorize(ctx, e.Run.ProjectID, actor.ActorID, core.ActionEdit)
	if err != nil {
		return WorkerResult{}, err
	}
	_, requiredRole, _ := nodeExecutionPolicy(e.Definition)
	if !workflowRoleSatisfies(currentRole, requiredRole) {
		return WorkerResult{}, core.ErrForbidden
	}
	manifest, err := buildManifestFromInputLineage(e)
	if err != nil {
		return WorkerResult{}, err
	}
	result, err := r.Evaluator.Evaluate(ctx, e)
	if err != nil {
		return WorkerResult{}, err
	}
	manifestCopy := manifest
	result.BuildManifest = &manifestCopy
	if !result.Passed && e.Definition.QualityGate != nil && e.Definition.QualityGate.Blocking {
		return WorkerResult{}, fmt.Errorf("blocking quality gate failed")
	}
	return WorkerResult{Disposition: ResultComplete, Output: mustJSON(result)}, nil
}

type PublishResult struct {
	URL          string `json:"url"`
	DeploymentID string `json:"deploymentId"`
}

type WorkflowPublishInput struct {
	QualityRunID      string             `json:"qualityRunId"`
	WorkspaceRevision domain.ArtifactRef `json:"workspaceRevision"`
	BuildManifest     BuildManifest      `json:"buildManifest"`
}

type Publisher interface {
	Publish(context.Context, string, string, string, string, WorkflowPublishInput) (PublishResult, error)
}
type PublisherFunc func(context.Context, string, string, string, string, WorkflowPublishInput) (PublishResult, error)

func (f PublisherFunc) Publish(ctx context.Context, projectID, runID, actorID, environment string, input WorkflowPublishInput) (PublishResult, error) {
	return f(ctx, projectID, runID, actorID, environment, input)
}

type PublishAuthorizer interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}
type PublishRunner struct {
	Publisher Publisher
	Access    PublishAuthorizer
}

func (r PublishRunner) Run(ctx context.Context, e Execution) (WorkerResult, error) {
	if r.Publisher == nil || r.Access == nil {
		return WorkerResult{}, fmt.Errorf("publisher and access control are required")
	}
	actor, err := e.ExecutionActor()
	if err != nil {
		return WorkerResult{}, err
	}
	if _, err := r.Access.Authorize(ctx, e.Run.ProjectID, actor.ActorID, core.ActionPublish); err != nil {
		return WorkerResult{}, err
	}
	if e.Definition.Publish != nil {
		role, err := r.Access.Authorize(ctx, e.Run.ProjectID, actor.ActorID, core.ActionView)
		if err != nil {
			return WorkerResult{}, err
		}
		if !workflowRoleSatisfies(role, core.Role(e.Definition.Publish.RequiredRole)) {
			return WorkerResult{}, core.ErrForbidden
		}
	}
	input, err := workflowPublishInputFromExecution(e)
	if err != nil {
		return WorkerResult{}, err
	}
	environment := "preview"
	if e.Definition.Publish != nil {
		environment = e.Definition.Publish.Environment
	}
	published, err := r.Publisher.Publish(ctx, e.Run.ProjectID, e.Run.ID, actor.ActorID, environment, input)
	if err != nil {
		return WorkerResult{}, err
	}
	return WorkerResult{Disposition: ResultComplete, Output: mustJSON(published)}, nil
}

func workflowPublishInputFromExecution(execution Execution) (WorkflowPublishInput, error) {
	inputs := make(map[string]WorkflowPublishInput)
	for _, binding := range execution.Inputs.Bindings() {
		for _, raw := range []json.RawMessage{binding.Value, binding.Output} {
			var quality QualityResult
			if err := json.Unmarshal(raw, &quality); err != nil || !quality.Passed || quality.BuildManifest == nil || quality.WorkspaceRevision == nil {
				continue
			}
			if _, err := uuid.Parse(quality.QualityRunID); err != nil || quality.WorkspaceRevision.Validate() != nil || quality.BuildManifest.Validate() != nil {
				continue
			}
			if quality.BuildManifest.ProjectID != execution.Run.ProjectID || quality.BuildManifest.RunID != execution.Run.ID {
				return WorkflowPublishInput{}, fmt.Errorf("incoming quality build manifest does not match the workflow run")
			}
			key := quality.QualityRunID + "\x00" + quality.WorkspaceRevision.ArtifactID + "\x00" + quality.WorkspaceRevision.RevisionID + "\x00" + quality.WorkspaceRevision.ContentHash + "\x00" + quality.BuildManifest.Hash
			inputs[key] = WorkflowPublishInput{
				QualityRunID: quality.QualityRunID, WorkspaceRevision: *quality.WorkspaceRevision,
				BuildManifest: *quality.BuildManifest,
			}
		}
	}
	if len(inputs) != 1 {
		return WorkflowPublishInput{}, fmt.Errorf("publish requires exactly one passing quality result from its typed inputs, got %d", len(inputs))
	}
	for _, input := range inputs {
		return input, nil
	}
	return WorkflowPublishInput{}, fmt.Errorf("incoming quality result is unavailable")
}

func toCoreVersionRef(ref domain.ArtifactRef) core.VersionRef {
	var anchor *string
	if ref.AnchorID != "" {
		value := ref.AnchorID
		anchor = &value
	}
	return core.VersionRef{ArtifactID: ref.ArtifactID, RevisionID: ref.RevisionID, ContentHash: ref.ContentHash, AnchorID: anchor}
}
func fromCoreVersionRef(ref core.VersionRef) domain.ArtifactRef {
	anchor := ""
	if ref.AnchorID != nil {
		anchor = *ref.AnchorID
	}
	return domain.ArtifactRef{ArtifactID: ref.ArtifactID, RevisionID: ref.RevisionID, ContentHash: ref.ContentHash, AnchorID: anchor}
}
func refsFromBundle(bundle core.WorkbenchBundle) []domain.ArtifactRef {
	refs := []domain.ArtifactRef{fromCoreVersionRef(bundle.BlueprintRevision), fromCoreVersionRef(bundle.PageSpecRevision), fromCoreVersionRef(bundle.PrototypeRevision)}
	for _, ref := range bundle.RequirementRevisions {
		refs = append(refs, fromCoreVersionRef(ref))
	}
	for _, ref := range bundle.ContractRevisions {
		refs = append(refs, fromCoreVersionRef(ref))
	}
	for _, ref := range bundle.DesignSystemRevisions {
		refs = append(refs, fromCoreVersionRef(ref))
	}
	return refs
}
func appendUniqueArtifactRef(refs []domain.ArtifactRef, candidate domain.ArtifactRef) []domain.ArtifactRef {
	for _, ref := range refs {
		if ref.Equal(candidate) {
			return refs
		}
	}
	return append(refs, candidate)
}
