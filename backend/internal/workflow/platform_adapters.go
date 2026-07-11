package workflow

import (
	"context"
	"crypto/sha256"
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
	HumanEditOutput     HumanEditOutputValidator
	TargetArtifacts     TargetArtifactInitializer
	RequirementBaseline RequirementBaselineCompiler
	WorkbenchCompletion WorkbenchCompletionValidator
	ReviewGate          ReviewGateVerifier
	Access              PublishAuthorizer
	FanOut              FanOutResolver
	BlueprintPages      FanOutResolver
	Quality             QualityEvaluator
	QualityManifest     QualityManifestResolver
	Publisher           Publisher
	DefaultModel        string
	BuildInstruction    string
	Clock               Clock
	IDs                 IDGenerator
}

// NewPlatformEngine exposes a single bootstrap seam without coupling app.go or
// the HTTP router to concrete runner implementations.
func NewPlatformEngine(dependencies PlatformDependencies) (*Engine, error) {
	if dependencies.Store == nil || dependencies.CoreProposals == nil || dependencies.Generation == nil || dependencies.Workbench == nil || dependencies.ArtifactInputs == nil || dependencies.HumanEditOutput == nil || dependencies.TargetArtifacts == nil || dependencies.WorkbenchCompletion == nil || dependencies.ReviewGate == nil || dependencies.Access == nil || dependencies.BlueprintPages == nil || dependencies.Quality == nil || dependencies.QualityManifest == nil || dependencies.Publisher == nil || dependencies.DefaultModel == "" {
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
	engine.RequireGovernedStarts = true
	engine.ManifestFreezer = CoreManifestFreezer{
		Proposals: dependencies.CoreProposals, Targets: dependencies.TargetArtifacts,
		RequirementBaseline: dependencies.RequirementBaseline,
	}
	engine.ArtifactInputs = dependencies.ArtifactInputs
	if resolver, ok := dependencies.ArtifactInputs.(StartArtifactKindResolver); ok {
		engine.StartArtifactKinds = resolver
	} else {
		return nil, fmt.Errorf("platform artifact input validator must resolve exact start artifact kinds")
	}
	engine.HumanEditOutput = dependencies.HumanEditOutput
	engine.WorkbenchCompletion = dependencies.WorkbenchCompletion
	engine.ReviewGate = dependencies.ReviewGate
	engine.ProposalDispatcher = GenerationProposalDispatcher{Generation: dependencies.Generation, DefaultModel: dependencies.DefaultModel}
	manifestHook := CoreWorkbenchManifestHook{Workbench: dependencies.Workbench, Proposals: dependencies.CoreProposals, Now: engine.now}
	engine.BuildManifestHook = manifestHook
	engine.ManifestCompilers = NewBuildManifestRegistry()
	if err := engine.ManifestCompilers.Register(ManifestCompilerCapability{
		ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1",
	}, manifestHook); err != nil {
		return nil, err
	}
	engine.ConditionEvaluator = DeclarativeConditionEvaluator{}
	registry := NewMapRegistry()
	if err := registry.Register(domain.NodeFanOut, FanOutRunner{Resolver: DefinitionFanOutResolver{
		DeliverySlices: dependencies.FanOut,
		BlueprintPages: dependencies.BlueprintPages,
	}}); err != nil {
		return nil, err
	}
	if err := registry.Register(domain.NodeTransform, RunnerFunc(func(_ context.Context, execution Execution) (WorkerResult, error) {
		if execution.Definition.Transform == nil || execution.Definition.Transform.Transform != "selection_passthrough" {
			return WorkerResult{}, fmt.Errorf("unsupported platform transform")
		}
		value, _, ok := execution.Inputs.FirstValue("default")
		if !ok {
			value = json.RawMessage(`{}`)
		}
		return WorkerResult{Disposition: ResultComplete, Output: append(json.RawMessage(nil), value...)}, nil
	})); err != nil {
		return nil, err
	}
	if err := registry.Register(domain.NodeWorkbenchBuild, GenerationWorkbenchRunner{Generation: dependencies.Generation, Workbench: dependencies.Workbench, DefaultModel: dependencies.DefaultModel, Instruction: dependencies.BuildInstruction}); err != nil {
		return nil, err
	}
	if err := registry.Register(domain.NodeQualityGate, QualityGateRunner{
		Evaluator: dependencies.Quality, ManifestResolver: dependencies.QualityManifest, Access: dependencies.Access,
	}); err != nil {
		return nil, err
	}
	if err := registry.Register(domain.NodePublish, PublishRunner{Publisher: dependencies.Publisher, Access: dependencies.Access}); err != nil {
		return nil, err
	}
	engine.Runners = registry
	engine.Capabilities = CurrentWorkflowExecutionProfileDescriptor().Capabilities
	if err := engine.SealProductionExecutionProfiles(); err != nil {
		return nil, err
	}
	return engine, nil
}

// CoreArtifactInputValidator enforces the declarative input gate against the
// exact artifact revisions referenced by the run manifest.
type CoreArtifactInputValidator struct {
	Database *gorm.DB
	Contents content.Store
}

func (v CoreArtifactInputValidator) ResolveStartArtifactKinds(ctx context.Context, manifest domain.InputManifest) ([]string, error) {
	metadata, err := v.ResolveStartArtifactMetadata(ctx, manifest)
	return metadata.Kinds, err
}

func (v CoreArtifactInputValidator) ResolveStartArtifactMetadata(ctx context.Context, manifest domain.InputManifest) (StartArtifactMetadata, error) {
	if v.Database == nil {
		return StartArtifactMetadata{}, fmt.Errorf("artifact input database is required")
	}
	if err := validateBlueprintSelectionStart(ctx, v.Database, v.Contents, manifest); err != nil {
		return StartArtifactMetadata{}, err
	}
	kinds := map[string]struct{}{}
	refs := artifactInputRefs(manifest)
	allApproved := true
	for _, ref := range refs {
		var row struct {
			Kind           string `gorm:"column:artifact_kind"`
			ProjectID      string `gorm:"column:artifact_project_id"`
			WorkflowStatus string `gorm:"column:workflow_status"`
		}
		if err := v.Database.WithContext(ctx).Table("artifact_revisions").
			Select("artifacts.kind AS artifact_kind, artifacts.project_id::text AS artifact_project_id, artifact_revisions.workflow_status").
			Joins("JOIN artifacts ON artifacts.id = artifact_revisions.artifact_id").
			Where("artifact_revisions.id = ? AND artifact_revisions.artifact_id = ? AND artifact_revisions.content_hash = ?", ref.RevisionID, ref.ArtifactID, ref.ContentHash).
			Take(&row).Error; err != nil {
			return StartArtifactMetadata{}, err
		}
		if row.ProjectID != manifest.ProjectID {
			return StartArtifactMetadata{}, fmt.Errorf("artifact input revision is outside the manifest project")
		}
		kinds[strings.TrimSpace(row.Kind)] = struct{}{}
		allApproved = allApproved && row.WorkflowStatus == "approved"
	}
	resolved := make([]string, 0, len(kinds))
	for kind := range kinds {
		resolved = append(resolved, kind)
	}
	sort.Strings(resolved)
	return StartArtifactMetadata{Kinds: resolved, Count: len(refs), AllApproved: allApproved && len(refs) > 0}, nil
}

func (v CoreArtifactInputValidator) Validate(ctx context.Context, execution Execution, manifest domain.InputManifest) (json.RawMessage, error) {
	if v.Database == nil || execution.Definition.ArtifactInput == nil {
		return nil, fmt.Errorf("artifact input database and node config are required")
	}
	config := execution.Definition.ArtifactInput
	if err := validateBlueprintSelectionStart(ctx, v.Database, v.Contents, manifest); err != nil {
		return nil, err
	}
	refs := artifactInputRefs(manifest)
	if len(refs) < config.MinimumArtifacts {
		return nil, fmt.Errorf("artifact input requires at least %d exact revisions", config.MinimumArtifacts)
	}
	if config.MaximumArtifacts > 0 && len(refs) > config.MaximumArtifacts {
		return nil, fmt.Errorf("artifact input accepts at most %d exact revisions", config.MaximumArtifacts)
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
		if manifest.JobType == core.BlueprintSelectionJobType &&
			source.Purpose != "blueprint_selection_root" && source.Purpose != "blueprint_selection_node" {
			continue
		}
		appendRef(source.Ref)
	}
	return refs
}

type workflowBlueprintSelectionPageBinding struct {
	NodeID    string              `json:"nodeId"`
	PageSpec  *domain.ArtifactRef `json:"pageSpec,omitempty"`
	Prototype *domain.ArtifactRef `json:"prototype,omitempty"`
}

type workflowBlueprintSelectionNode struct {
	ID             string   `json:"id"`
	Key            string   `json:"key"`
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	Route          string   `json:"route,omitempty"`
	UserGoal       string   `json:"userGoal,omitempty"`
	RequirementIDs []string `json:"requirementIds,omitempty"`
}

type workflowBlueprintSelectionScope struct {
	SchemaVersion int                                     `json:"schemaVersion"`
	SelectionID   string                                  `json:"selectionId"`
	Blueprint     domain.ArtifactRef                      `json:"blueprint"`
	NodeIDs       []string                                `json:"nodeIds"`
	Nodes         []workflowBlueprintSelectionNode        `json:"nodes"`
	PageBindings  []workflowBlueprintSelectionPageBinding `json:"pageBindings"`
}

func blueprintSelectionScope(manifest domain.InputManifest) (workflowBlueprintSelectionScope, bool, error) {
	if manifest.JobType != core.BlueprintSelectionJobType {
		return workflowBlueprintSelectionScope{}, false, nil
	}
	if err := manifest.Validate(); err != nil {
		return workflowBlueprintSelectionScope{}, true, err
	}
	if manifest.OutputSchemaVersion != "blueprint-selection/v1" {
		return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection manifest schema is unsupported")
	}
	var constraints struct {
		BlueprintSelection workflowBlueprintSelectionScope `json:"blueprintSelection"`
	}
	if err := json.Unmarshal(manifest.Constraints, &constraints); err != nil {
		return workflowBlueprintSelectionScope{}, true, fmt.Errorf("decode Blueprint selection scope: %w", err)
	}
	scope := constraints.BlueprintSelection
	if scope.SchemaVersion != 1 || strings.TrimSpace(scope.SelectionID) == "" ||
		manifest.DeliverySliceID != scope.SelectionID || scope.Blueprint.Validate() != nil ||
		scope.Blueprint.AnchorID != "" || len(scope.NodeIDs) == 0 || len(scope.NodeIDs) > 100 {
		return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection scope identity is invalid")
	}
	rootCount := 0
	nodeSources := map[string]bool{}
	otherSources := map[string]bool{}
	for _, source := range manifest.Sources {
		key := exactWorkflowArtifactRefKey(source.Ref)
		otherSources[key] = true
		switch source.Purpose {
		case "blueprint_selection_root":
			if !source.Ref.Equal(scope.Blueprint) {
				return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection root source drifted")
			}
			rootCount++
		case "blueprint_selection_node":
			if source.Ref.ArtifactID != scope.Blueprint.ArtifactID ||
				source.Ref.RevisionID != scope.Blueprint.RevisionID ||
				source.Ref.ContentHash != scope.Blueprint.ContentHash || source.Ref.AnchorID == "" {
				return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection node source drifted")
			}
			nodeSources[source.Ref.AnchorID] = true
		}
	}
	if rootCount != 1 || len(nodeSources) != len(scope.NodeIDs) {
		return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection sources do not match its node scope")
	}
	seenNodes := map[string]bool{}
	for _, nodeID := range scope.NodeIDs {
		if strings.TrimSpace(nodeID) == "" || seenNodes[nodeID] || !nodeSources[nodeID] {
			return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection node anchors are incomplete")
		}
		seenNodes[nodeID] = true
	}
	if len(scope.Nodes) != len(scope.NodeIDs) {
		return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection semantic node snapshots are incomplete")
	}
	pageNodes := map[string]bool{}
	seenSnapshots := map[string]bool{}
	for _, node := range scope.Nodes {
		nodeID, kind := strings.TrimSpace(node.ID), strings.TrimSpace(node.Kind)
		if nodeID == "" || kind == "" || !seenNodes[nodeID] || seenSnapshots[nodeID] {
			return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection semantic node snapshot is invalid")
		}
		seenSnapshots[nodeID] = true
		if kind == "page" {
			pageNodes[nodeID] = true
		}
	}
	if len(pageNodes) == 0 {
		return workflowBlueprintSelectionScope{}, true, fmt.Errorf("application Blueprint selection requires at least one selected Page")
	}
	seenBindings := map[string]bool{}
	for _, binding := range scope.PageBindings {
		if !pageNodes[binding.NodeID] || seenBindings[binding.NodeID] {
			return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection page binding is outside the node scope")
		}
		seenBindings[binding.NodeID] = true
		for _, ref := range []*domain.ArtifactRef{binding.PageSpec, binding.Prototype} {
			if ref != nil && (ref.Validate() != nil || ref.AnchorID != "" || !otherSources[exactWorkflowArtifactRefKey(*ref)]) {
				return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection page binding is not frozen in manifest sources")
			}
		}
		if binding.PageSpec == nil || binding.Prototype == nil {
			return workflowBlueprintSelectionScope{}, true, fmt.Errorf("every selected Page requires exact PageSpec and Prototype bindings")
		}
	}
	if len(seenBindings) != len(pageNodes) {
		return workflowBlueprintSelectionScope{}, true, fmt.Errorf("Blueprint selection page bindings are incomplete")
	}
	return scope, true, nil
}

func exactWorkflowArtifactRefKey(ref domain.ArtifactRef) string {
	return ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" + ref.AnchorID
}

type workflowSelectionRevisionSource struct {
	Ref      domain.ArtifactRef
	Purpose  string
	Required bool
}

type workflowSelectionArtifactSnapshot struct {
	Ref                      domain.ArtifactRef
	ProjectID                string
	Kind                     string
	Lifecycle                string
	WorkflowStatus           string
	SyncStatus               string
	LatestApprovedRevisionID string
	Sources                  []workflowSelectionRevisionSource
	Content                  json.RawMessage
}

type workflowSelectionArtifactResolver func(context.Context, domain.ArtifactRef) (workflowSelectionArtifactSnapshot, error)

func validateBlueprintSelectionStart(ctx context.Context, database *gorm.DB, contents content.Store, manifest domain.InputManifest) error {
	if manifest.JobType != core.BlueprintSelectionJobType {
		return nil
	}
	if database == nil || contents == nil {
		return fmt.Errorf("Blueprint selection validation database and content store are required")
	}
	return validateBlueprintSelectionStartWithResolver(ctx, manifest, func(ctx context.Context, ref domain.ArtifactRef) (workflowSelectionArtifactSnapshot, error) {
		return loadWorkflowSelectionArtifactSnapshot(ctx, database, contents, ref)
	})
}

func validateBlueprintSelectionStartWithResolver(
	ctx context.Context,
	manifest domain.InputManifest,
	resolve workflowSelectionArtifactResolver,
) error {
	scope, selection, err := blueprintSelectionScope(manifest)
	if err != nil || !selection {
		return err
	}
	if resolve == nil {
		return fmt.Errorf("Blueprint selection artifact resolver is required")
	}
	blueprint, err := resolve(ctx, scope.Blueprint)
	if err != nil {
		return err
	}
	if err := requireCurrentSelectionArtifact(blueprint, manifest.ProjectID, "blueprint", scope.Blueprint); err != nil {
		return fmt.Errorf("Blueprint selection root: %w", err)
	}
	actualNodes, _, err := core.DecodeBlueprintSemanticGraph(blueprint.Content)
	if err != nil {
		return fmt.Errorf("Blueprint selection root content: %w", err)
	}
	actualByID := make(map[string]core.BlueprintSemanticNode, len(actualNodes))
	for _, node := range actualNodes {
		actualByID[node.ID] = node
	}
	pageNodes := map[string]bool{}
	for _, node := range scope.Nodes {
		actual, exists := actualByID[strings.TrimSpace(node.ID)]
		if !exists || actual.Kind != strings.TrimSpace(node.Kind) || actual.Key != strings.TrimSpace(node.Key) ||
			actual.Title != strings.TrimSpace(node.Title) || actual.Route != strings.TrimSpace(node.Route) ||
			actual.UserGoal != strings.TrimSpace(node.UserGoal) || !sameStrings(actual.RequirementIDs, node.RequirementIDs) {
			return fmt.Errorf("Blueprint selection semantic node %q differs from the pinned Blueprint content", node.ID)
		}
		if strings.TrimSpace(node.Kind) == "page" {
			pageNodes[strings.TrimSpace(node.ID)] = true
		}
	}
	expectedSources := make([]domain.ManifestSource, 0, len(manifest.Sources))
	seenExpectedRefs := map[string]bool{}
	appendExpectedSource := func(ref domain.ArtifactRef, purpose string) {
		key := exactWorkflowArtifactRefKey(ref)
		if seenExpectedRefs[key] {
			return
		}
		seenExpectedRefs[key] = true
		expectedSources = append(expectedSources, domain.ManifestSource{Ref: ref, Purpose: purpose})
	}
	appendExpectedSource(scope.Blueprint, "blueprint_selection_root")
	for _, nodeID := range scope.NodeIDs {
		anchored := scope.Blueprint
		anchored.AnchorID = nodeID
		appendExpectedSource(anchored, "blueprint_selection_node")
	}
	bindings := make(map[string]workflowBlueprintSelectionPageBinding, len(scope.PageBindings))
	for _, binding := range scope.PageBindings {
		if !pageNodes[binding.NodeID] {
			return fmt.Errorf("Blueprint selection contains a binding for non-selected Page %q", binding.NodeID)
		}
		if _, duplicate := bindings[binding.NodeID]; duplicate {
			return fmt.Errorf("Blueprint selection contains duplicate Page binding %q", binding.NodeID)
		}
		bindings[binding.NodeID] = binding
		if binding.PageSpec != nil {
			appendExpectedSource(*binding.PageSpec, "selected_page_spec")
		}
		if binding.Prototype != nil {
			appendExpectedSource(*binding.Prototype, "selected_prototype")
		}
	}
	if len(bindings) != len(pageNodes) {
		return fmt.Errorf("Blueprint selection Page bindings do not exactly match selected Pages")
	}
	for nodeID := range pageNodes {
		binding, exists := bindings[nodeID]
		if !exists || binding.PageSpec == nil || binding.Prototype == nil {
			return fmt.Errorf("selected Page %q is missing an exact PageSpec or Prototype binding", nodeID)
		}
		pageSpec, err := resolve(ctx, *binding.PageSpec)
		if err != nil {
			return err
		}
		if err := requireCurrentSelectionArtifact(pageSpec, manifest.ProjectID, "page_spec", *binding.PageSpec); err != nil {
			return fmt.Errorf("selected Page %q PageSpec: %w", nodeID, err)
		}
		expectedBlueprint := scope.Blueprint
		expectedBlueprint.AnchorID = nodeID
		if !hasOnlyExactRequiredSelectionSource(pageSpec.Sources, "blueprint", expectedBlueprint) {
			return fmt.Errorf("selected Page %q PageSpec does not derive from the exact Blueprint page anchor", nodeID)
		}
		prototype, err := resolve(ctx, *binding.Prototype)
		if err != nil {
			return err
		}
		if err := requireCurrentSelectionArtifact(prototype, manifest.ProjectID, "prototype", *binding.Prototype); err != nil {
			return fmt.Errorf("selected Page %q Prototype: %w", nodeID, err)
		}
		if !hasOnlyExactRequiredSelectionSource(prototype.Sources, "page_spec", *binding.PageSpec) {
			return fmt.Errorf("selected Page %q Prototype does not derive from the exact PageSpec", nodeID)
		}
	}
	for _, source := range blueprint.Sources {
		contextArtifact, err := resolve(ctx, source.Ref)
		if err != nil {
			return fmt.Errorf("resolve Blueprint selection context: %w", err)
		}
		if !contextArtifact.Ref.Equal(source.Ref) || contextArtifact.ProjectID != manifest.ProjectID || contextArtifact.WorkflowStatus != "approved" {
			return fmt.Errorf("Blueprint selection context is no longer an approved same-project revision")
		}
		appendExpectedSource(source.Ref, "selection_context")
	}
	if !sameExactManifestSources(manifest.Sources, expectedSources) {
		return fmt.Errorf("Blueprint selection manifest sources differ from the server-resolved frozen selection")
	}
	selectionID, err := workflowBlueprintSelectionIdentity(scope, manifest.Sources)
	if err != nil {
		return err
	}
	if selectionID != scope.SelectionID {
		return fmt.Errorf("Blueprint selection identity does not match its canonical frozen inputs")
	}
	return nil
}

func sameExactManifestSources(left, right []domain.ManifestSource) bool {
	counts := func(values []domain.ManifestSource) map[string]int {
		result := make(map[string]int, len(values))
		for _, source := range values {
			result[source.Purpose+"\x00"+exactWorkflowArtifactRefKey(source.Ref)]++
		}
		return result
	}
	leftCounts, rightCounts := counts(left), counts(right)
	if len(leftCounts) != len(rightCounts) || len(left) != len(right) {
		return false
	}
	for key, count := range leftCounts {
		if rightCounts[key] != count {
			return false
		}
	}
	return true
}

func workflowBlueprintSelectionIdentity(scope workflowBlueprintSelectionScope, sources []domain.ManifestSource) (string, error) {
	toVersionRef := func(ref domain.ArtifactRef) core.VersionRef {
		value := core.VersionRef{ArtifactID: ref.ArtifactID, RevisionID: ref.RevisionID, ContentHash: ref.ContentHash}
		if ref.AnchorID != "" {
			anchor := ref.AnchorID
			value.AnchorID = &anchor
		}
		return value
	}
	bindings := make([]core.BlueprintSelectionPageBinding, 0, len(scope.PageBindings))
	for _, binding := range scope.PageBindings {
		converted := core.BlueprintSelectionPageBinding{NodeID: binding.NodeID}
		if binding.PageSpec != nil {
			value := toVersionRef(*binding.PageSpec)
			converted.PageSpec = &value
		}
		if binding.Prototype != nil {
			value := toVersionRef(*binding.Prototype)
			converted.Prototype = &value
		}
		bindings = append(bindings, converted)
	}
	convertedSources := make([]core.ManifestSourceInput, 0, len(sources))
	for _, source := range sources {
		convertedSources = append(convertedSources, core.ManifestSourceInput{Ref: toVersionRef(source.Ref), Purpose: source.Purpose})
	}
	return core.BlueprintSelectionIdentity(toVersionRef(scope.Blueprint), scope.NodeIDs, bindings, convertedSources)
}

func requireCurrentSelectionArtifact(snapshot workflowSelectionArtifactSnapshot, projectID, kind string, expected domain.ArtifactRef) error {
	if !snapshot.Ref.Equal(expected) || snapshot.ProjectID != projectID || snapshot.Kind != kind || snapshot.Lifecycle != "active" {
		return fmt.Errorf("artifact identity, project, kind, or lifecycle drifted")
	}
	if snapshot.WorkflowStatus != "approved" || snapshot.LatestApprovedRevisionID != expected.RevisionID || snapshot.SyncStatus != "current" {
		return fmt.Errorf("artifact is not its current latest-approved revision")
	}
	return nil
}

func hasOnlyExactRequiredSelectionSource(sources []workflowSelectionRevisionSource, purpose string, expected domain.ArtifactRef) bool {
	count := 0
	for _, source := range sources {
		if strings.TrimSpace(source.Purpose) != purpose || !source.Required {
			continue
		}
		count++
		if !source.Ref.Equal(expected) {
			return false
		}
	}
	return count == 1
}

func loadWorkflowSelectionArtifactSnapshot(
	ctx context.Context,
	database *gorm.DB,
	contents content.Store,
	ref domain.ArtifactRef,
) (workflowSelectionArtifactSnapshot, error) {
	var row struct {
		ProjectID                string `gorm:"column:project_id"`
		Kind                     string `gorm:"column:kind"`
		Lifecycle                string `gorm:"column:lifecycle"`
		WorkflowStatus           string `gorm:"column:workflow_status"`
		SyncStatus               string `gorm:"column:sync_status"`
		LatestApprovedRevisionID string `gorm:"column:latest_approved_revision_id"`
		ContentRef               string `gorm:"column:content_ref"`
		ContentHash              string `gorm:"column:content_hash"`
	}
	if err := database.WithContext(ctx).Table("artifact_revisions AS revision").
		Select("artifact.project_id::text AS project_id, artifact.kind, artifact.lifecycle, revision.workflow_status, COALESCE(health.sync_status, '') AS sync_status, COALESCE(artifact.latest_approved_revision_id::text, '') AS latest_approved_revision_id, revision.content_ref, revision.content_hash").
		Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Joins("LEFT JOIN artifact_health AS health ON health.artifact_id = artifact.id").
		Where("revision.id = ? AND revision.artifact_id = ? AND revision.content_hash = ?", ref.RevisionID, ref.ArtifactID, ref.ContentHash).
		Take(&row).Error; err != nil {
		return workflowSelectionArtifactSnapshot{}, err
	}
	stored, err := contents.Get(ctx, row.ContentRef, row.ContentHash)
	if err != nil {
		return workflowSelectionArtifactSnapshot{}, err
	}
	var sourceRows []struct {
		ArtifactID  string  `gorm:"column:artifact_id"`
		RevisionID  string  `gorm:"column:revision_id"`
		ContentHash string  `gorm:"column:content_hash"`
		AnchorID    *string `gorm:"column:anchor_id"`
		Purpose     string  `gorm:"column:purpose"`
		Required    bool    `gorm:"column:required"`
	}
	if err := database.WithContext(ctx).Table("artifact_revision_sources AS source").
		Select("source.source_artifact_id::text AS artifact_id, source.source_revision_id::text AS revision_id, source.source_content_hash AS content_hash, source.source_anchor_id AS anchor_id, source.purpose, source.required").
		Where("source.revision_id = ?", ref.RevisionID).
		Order("source.ordinal ASC, source.source_artifact_id ASC").Scan(&sourceRows).Error; err != nil {
		return workflowSelectionArtifactSnapshot{}, err
	}
	snapshot := workflowSelectionArtifactSnapshot{
		Ref: ref, ProjectID: row.ProjectID, Kind: row.Kind, Lifecycle: row.Lifecycle,
		WorkflowStatus: row.WorkflowStatus, SyncStatus: row.SyncStatus,
		LatestApprovedRevisionID: row.LatestApprovedRevisionID, Content: stored.Payload,
	}
	for _, source := range sourceRows {
		anchor := ""
		if source.AnchorID != nil {
			anchor = *source.AnchorID
		}
		snapshot.Sources = append(snapshot.Sources, workflowSelectionRevisionSource{
			Ref:     domain.ArtifactRef{ArtifactID: source.ArtifactID, RevisionID: source.RevisionID, ContentHash: source.ContentHash, AnchorID: anchor},
			Purpose: source.Purpose, Required: source.Required,
		})
	}
	return snapshot, nil
}

func loadRunBlueprintSelection(
	ctx context.Context,
	proposals CoreProposalAPI,
	execution Execution,
) (workflowBlueprintSelectionScope, bool, error) {
	if execution.Run.InputManifest == nil {
		return workflowBlueprintSelectionScope{}, false, nil
	}
	if proposals == nil {
		return workflowBlueprintSelectionScope{}, false, nil
	}
	manifest, err := proposals.GetManifest(ctx, execution.Run.InputManifest.ID, execution.Run.StartedBy)
	if err != nil {
		return workflowBlueprintSelectionScope{}, false, err
	}
	if manifest.Ref() != *execution.Run.InputManifest || manifest.ProjectID != execution.Run.ProjectID {
		return workflowBlueprintSelectionScope{}, false, fmt.Errorf("workflow run input manifest drifted")
	}
	return blueprintSelectionScope(manifest)
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
	if len(config.AllowedKinds) > 0 {
		allowed := false
		for _, candidate := range config.AllowedKinds {
			if strings.TrimSpace(candidate) == kind {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("artifact kind %s is not allowed by the exact input contract", kind)
		}
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

type HumanEditArtifactAPI interface {
	Get(context.Context, string, string, bool) (core.VersionedArtifact, error)
	GetRevision(context.Context, string, string) (core.ArtifactRevision, error)
}

type HumanEditProposalAPI interface {
	GetManifest(context.Context, string, string) (domain.InputManifest, error)
	GetProposal(context.Context, string, string) (domain.OutputProposal, error)
}

// CoreHumanEditOutputValidator resolves every client-supplied revision against
// the authoritative artifact/proposal stores. The engine consumes only this
// result when updating slice lineage; broad ArtifactType values never decide
// whether a ref is a PageSpec or Prototype.
type CoreHumanEditOutputValidator struct {
	Artifacts HumanEditArtifactAPI
	Proposals HumanEditProposalAPI
}

func (v CoreHumanEditOutputValidator) ValidateHumanEdit(
	ctx context.Context,
	execution Execution,
	output json.RawMessage,
	actorID string,
) (HumanEditValidation, error) {
	if v.Artifacts == nil || execution.Definition.HumanEdit == nil {
		return HumanEditValidation{}, fmt.Errorf("human edit artifact service and node config are required")
	}
	refs, primary, err := humanEditOutputRefs(output)
	if err != nil {
		return HumanEditValidation{}, err
	}
	expectedKind := strings.TrimSpace(execution.Definition.HumanEdit.ArtifactKind)
	if expectedKind != "" && len(refs) != 1 {
		return HumanEditValidation{}, humanEditValidationError("artifactRevisions", "exact-kind human edit nodes must submit exactly one artifact revision")
	}
	resolvedKind := ""
	revisions := make(map[string]core.ArtifactRevision, len(refs))
	for index, ref := range refs {
		if ref.AnchorID != "" {
			return HumanEditValidation{}, humanEditValidationError(
				fmt.Sprintf("artifactRevisions[%d].anchorId", index),
				"human edit output must pin a whole artifact revision",
			)
		}
		artifact, err := v.Artifacts.Get(ctx, ref.ArtifactID, actorID, false)
		if err != nil {
			return HumanEditValidation{}, humanEditValidationError(fmt.Sprintf("artifactRevisions[%d].artifactId", index), "artifact is unavailable to this actor")
		}
		if artifact.Artifact.ID != ref.ArtifactID || artifact.Artifact.ProjectID != execution.Run.ProjectID {
			return HumanEditValidation{}, humanEditValidationError(fmt.Sprintf("artifactRevisions[%d].artifactId", index), "artifact belongs to another project")
		}
		kind := artifact.Artifact.Kind
		if expectedKind != "" {
			if kind != expectedKind {
				return HumanEditValidation{}, humanEditValidationError(fmt.Sprintf("artifactRevisions[%d]", index), fmt.Sprintf("artifact kind %q does not match required kind %q", kind, expectedKind))
			}
		} else if workflowArtifactType(kind) != execution.Definition.HumanEdit.ArtifactType {
			return HumanEditValidation{}, humanEditValidationError(fmt.Sprintf("artifactRevisions[%d]", index), fmt.Sprintf("artifact kind %q does not match required artifact type", kind))
		}
		if resolvedKind == "" {
			resolvedKind = kind
		} else if resolvedKind != kind {
			return HumanEditValidation{}, humanEditValidationError("artifactRevisions", "human edit output cannot mix exact artifact kinds")
		}
		revision, err := v.Artifacts.GetRevision(ctx, ref.RevisionID, actorID)
		if err != nil {
			return HumanEditValidation{}, humanEditValidationError(fmt.Sprintf("artifactRevisions[%d].revisionId", index), "artifact revision is unavailable to this actor")
		}
		if revision.ID != ref.RevisionID || revision.ArtifactID != ref.ArtifactID || revision.ContentHash != ref.ContentHash {
			return HumanEditValidation{}, humanEditValidationError(fmt.Sprintf("artifactRevisions[%d]", index), "artifact revision id, artifact id, and content hash must match exactly")
		}
		revisions[ref.RevisionID] = revision
	}
	primaryRevision, exists := revisions[primary.RevisionID]
	if !exists || primaryRevision.ArtifactID != primary.ArtifactID {
		return HumanEditValidation{}, humanEditValidationError("artifactRevision", "primary artifact revision was not validated")
	}
	if resolvedKind == "project_brief" {
		if err := v.validateProjectBriefEntry(ctx, execution, primary, actorID); err != nil {
			return HumanEditValidation{}, err
		}
	}
	if err := v.validateProposalLineage(ctx, execution, primaryRevision, primary, actorID); err != nil {
		return HumanEditValidation{}, err
	}
	return HumanEditValidation{ArtifactRefs: refs, Primary: primary, ArtifactKind: resolvedKind}, nil
}

func humanEditOutputRefs(output json.RawMessage) ([]domain.ArtifactRef, domain.ArtifactRef, error) {
	var envelope struct {
		ArtifactRevision *domain.ArtifactRef `json:"artifactRevision"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		return nil, domain.ArtifactRef{}, err
	}
	refs, err := artifactRefsFromNodeOutput(output)
	if err != nil {
		return nil, domain.ArtifactRef{}, err
	}
	if len(refs) == 0 {
		return nil, domain.ArtifactRef{}, humanEditValidationError("artifactRevision", "human edit output must pin at least one artifact revision")
	}
	if envelope.ArtifactRevision != nil {
		if err := envelope.ArtifactRevision.Validate(); err != nil {
			return nil, domain.ArtifactRef{}, err
		}
		return refs, *envelope.ArtifactRevision, nil
	}
	if len(refs) != 1 {
		return nil, domain.ArtifactRef{}, humanEditValidationError("artifactRevision", "a primary artifact revision is required when multiple revisions are submitted")
	}
	return refs, refs[0], nil
}

func (v CoreHumanEditOutputValidator) validateProjectBriefEntry(
	ctx context.Context,
	execution Execution,
	primary domain.ArtifactRef,
	actorID string,
) error {
	if v.Proposals == nil || execution.Run.InputManifest == nil {
		return humanEditValidationError("artifactRevision.artifactId", "Project Brief edit requires the immutable workflow entry manifest")
	}
	manifest, err := v.Proposals.GetManifest(ctx, execution.Run.InputManifest.ID, actorID)
	if err != nil || manifest.Ref() != *execution.Run.InputManifest || manifest.ProjectID != execution.Run.ProjectID {
		return humanEditValidationError("inputManifest", "workflow entry manifest does not match the run")
	}
	var entry *domain.ArtifactRef
	if manifest.BaseRevision != nil {
		value := *manifest.BaseRevision
		entry = &value
	} else {
		for _, source := range manifest.Sources {
			if source.Purpose != "project_brief" {
				continue
			}
			if entry != nil && entry.ArtifactID != source.Ref.ArtifactID {
				return humanEditValidationError("inputManifest.sources", "workflow entry has ambiguous Project Brief artifacts")
			}
			value := source.Ref
			entry = &value
		}
	}
	if entry == nil || primary.ArtifactID != entry.ArtifactID {
		return humanEditValidationError("artifactRevision.artifactId", "Project Brief edit must retain the workflow entry artifact id")
	}
	return nil
}

type humanEditProposalPin struct {
	proposal domain.ProposalRef
	manifest domain.ManifestRef
}

func (v CoreHumanEditOutputValidator) validateProposalLineage(
	ctx context.Context,
	execution Execution,
	revision core.ArtifactRevision,
	primary domain.ArtifactRef,
	actorID string,
) error {
	pins := make(map[string]humanEditProposalPin)
	for _, binding := range execution.Inputs.Bindings() {
		if binding.Source.OutputProposal != nil && binding.Source.InputManifest == nil {
			return humanEditValidationError("input.outputProposal", "proposal input is missing its immutable manifest ref")
		}
	}
	for _, lineage := range execution.Inputs.ProposalPins() {
		pin := humanEditProposalPin{proposal: lineage.Proposal, manifest: lineage.Manifest}
		key := lineage.ProducerNodeKey + "\x00" + lineage.ProducerDefinitionNodeID + "\x00" + pin.proposal.ID + "\x00" + pin.proposal.PayloadHash
		if existing, duplicate := pins[key]; duplicate && existing.manifest != pin.manifest {
			return humanEditValidationError("input.outputProposal", "proposal is bound to conflicting manifests")
		}
		pins[key] = pin
	}
	if len(pins) == 0 {
		if execution.Workflow.InputContract != nil {
			return humanEditValidationError("input.outputProposal", "governed human edit requires exactly one proposal input")
		}
		return nil
	}
	if len(pins) != 1 || v.Proposals == nil {
		return humanEditValidationError("input.outputProposal", "human edit requires exactly one resolvable proposal input")
	}
	var pin humanEditProposalPin
	for _, candidate := range pins {
		pin = candidate
	}
	proposal, err := v.Proposals.GetProposal(ctx, pin.proposal.ID, actorID)
	if err != nil || proposal.ValidatePayloadHash() != nil || proposal.PayloadHash != pin.proposal.PayloadHash {
		return humanEditValidationError("input.outputProposal", "proposal id and payload hash do not match the stored proposal")
	}
	if proposal.ProjectID != execution.Run.ProjectID || proposal.ArtifactID != primary.ArtifactID || proposal.BaseRevision.ArtifactID != primary.ArtifactID {
		return humanEditValidationError("artifactRevision.artifactId", "submitted revision does not target the pinned proposal artifact")
	}
	if proposal.Manifest != pin.manifest {
		return humanEditValidationError("input.outputProposal.manifest", "proposal manifest does not match the frozen input binding")
	}
	manifest, err := v.Proposals.GetManifest(ctx, pin.manifest.ID, actorID)
	if err != nil || manifest.Ref() != pin.manifest || manifest.ProjectID != execution.Run.ProjectID || manifest.BaseRevision == nil || !manifest.BaseRevision.Equal(proposal.BaseRevision) {
		return humanEditValidationError("input.outputProposal.manifest", "proposal base revision is not pinned by the exact input manifest")
	}
	return v.requireRevisionDescendant(ctx, revision, proposal.BaseRevision, proposal.ID, actorID)
}

func (v CoreHumanEditOutputValidator) requireRevisionDescendant(
	ctx context.Context,
	revision core.ArtifactRevision,
	base domain.ArtifactRef,
	proposalID string,
	actorID string,
) error {
	if base.AnchorID != "" || revision.ID == base.RevisionID {
		return humanEditValidationError("artifactRevision.revisionId", "submitted revision must be a descendant of the proposal base revision")
	}
	seen := map[string]struct{}{revision.ID: {}}
	current := revision
	containsProposalRevision := current.ProposalID != nil && *current.ProposalID == proposalID
	for depth := 0; depth < 10000 && current.ParentRevisionID != nil; depth++ {
		parentID := *current.ParentRevisionID
		if _, cycle := seen[parentID]; cycle {
			return humanEditValidationError("artifactRevision.revisionId", "artifact revision ancestry contains a cycle")
		}
		seen[parentID] = struct{}{}
		parent, err := v.Artifacts.GetRevision(ctx, parentID, actorID)
		if err != nil || parent.ArtifactID != revision.ArtifactID {
			return humanEditValidationError("artifactRevision.revisionId", "artifact revision ancestry is unavailable or crosses artifacts")
		}
		if parent.ID == base.RevisionID {
			if parent.ContentHash != base.ContentHash || parent.ArtifactID != base.ArtifactID {
				return humanEditValidationError("artifactRevision.revisionId", "proposal base revision hash does not match ancestry")
			}
			if !containsProposalRevision {
				return humanEditValidationError("artifactRevision.proposalId", "revision ancestry does not contain the bound output proposal")
			}
			return nil
		}
		if parent.ProposalID != nil && *parent.ProposalID == proposalID {
			containsProposalRevision = true
		}
		current = parent
	}
	return humanEditValidationError("artifactRevision.revisionId", "submitted revision is not descended from the proposal base revision")
}

func humanEditValidationError(field, message string) error {
	return &domain.DomainError{Kind: domain.ErrValidation, Field: field, Message: message}
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
	workflowRunID, err := uuid.Parse(execution.Run.ID)
	if err != nil {
		return "", fmt.Errorf("workflow run id is invalid")
	}
	manifestGroupKey := strings.TrimSpace(buildManifest.ManifestGroupKey)
	if manifestGroupKey == "" {
		// Migration 000012 deterministically assigns pre-group workflow roots to
		// this compatibility group. Their already-frozen BuildManifest hash cannot
		// be rewritten, so completion accepts only the corresponding DB marker.
		manifestGroupKey = "legacy"
	} else if parsed, err := uuid.Parse(manifestGroupKey); err != nil || parsed.String() != manifestGroupKey {
		return "", fmt.Errorf("frozen build manifest group is invalid")
	}
	allowedRoots := map[uuid.UUID]bool{}
	allowedRootOrder := make([]uuid.UUID, 0, len(buildManifest.BundleIDs))
	for rootOrdinal, bundleID := range buildManifest.BundleIDs {
		parsedBundleID, err := uuid.Parse(bundleID)
		if err != nil {
			return "", fmt.Errorf("frozen build manifest bundle id is invalid")
		}
		var manifest storage.ApplicationBuildManifestModel
		if err := v.Database.WithContext(ctx).
			Where("id = ? AND project_id = ?", parsedBundleID, execution.Run.ProjectID).
			Take(&manifest).Error; err != nil {
			return "", err
		}
		if manifest.WorkflowRunID == nil || *manifest.WorkflowRunID != workflowRunID {
			return "", fmt.Errorf("frozen build manifest does not belong to the workflow run")
		}
		if manifest.ManifestGroupKey == nil || *manifest.ManifestGroupKey != manifestGroupKey {
			return "", fmt.Errorf("frozen build manifest does not belong to the manifest compiler group")
		}
		if manifest.RootOrdinal == nil || *manifest.RootOrdinal != rootOrdinal {
			return "", fmt.Errorf("frozen build manifest root ordinal does not match bundle order")
		}
		if manifestGroupKey != "legacy" &&
			(manifest.DeliverySliceID == nil || *manifest.DeliverySliceID != buildManifest.SliceIDs[rootOrdinal]) {
			return "", fmt.Errorf("frozen build manifest delivery slice does not match bundle order")
		}
		rootID := manifest.RootManifestID
		if rootID == uuid.Nil {
			rootID = manifest.ID
		}
		if allowedRoots[rootID] {
			return "", fmt.Errorf("frozen build manifest contains duplicate root lineage %s", rootID)
		}
		if err := validateBuildManifestRootLineage(ctx, v.Database, manifest, rootID, manifest.ProjectID, workflowRunID); err != nil {
			return "", err
		}
		allowedRoots[rootID] = true
		allowedRootOrder = append(allowedRootOrder, rootID)
	}
	if len(envelope.ImplementationProposalIDs) != len(allowedRoots) {
		return "", fmt.Errorf("workbench completion requires exactly one applied implementation proposal for every frozen bundle")
	}
	seen, coveredRoots := map[string]bool{}, map[uuid.UUID]bool{}
	for proposalIndex, proposalID := range envelope.ImplementationProposalIDs {
		parsedProposalID, err := uuid.Parse(proposalID)
		if err != nil {
			return "", fmt.Errorf("implementation proposal id is invalid")
		}
		proposalID = parsedProposalID.String()
		envelope.ImplementationProposalIDs[proposalIndex] = proposalID
		if seen[proposalID] {
			return "", fmt.Errorf("duplicate implementation proposal %s", proposalID)
		}
		seen[proposalID] = true
		var proposal storage.ImplementationProposalModel
		if err := v.Database.WithContext(ctx).Where("id = ? AND project_id = ?", proposalID, execution.Run.ProjectID).Take(&proposal).Error; err != nil {
			return "", err
		}
		var proposalManifest storage.ApplicationBuildManifestModel
		if err := v.Database.WithContext(ctx).
			Where("id = ? AND project_id = ?", proposal.BuildManifestID, execution.Run.ProjectID).
			Take(&proposalManifest).Error; err != nil {
			return "", err
		}
		if proposalManifest.WorkflowRunID == nil || *proposalManifest.WorkflowRunID != workflowRunID {
			return "", fmt.Errorf("implementation proposal %s belongs to another workflow run", proposalID)
		}
		if proposalManifest.ManifestGroupKey == nil || *proposalManifest.ManifestGroupKey != manifestGroupKey {
			return "", fmt.Errorf("implementation proposal %s belongs to another manifest compiler group", proposalID)
		}
		if proposalManifest.RootOrdinal == nil || *proposalManifest.RootOrdinal != proposalIndex {
			return "", fmt.Errorf("implementation proposal %s has the wrong frozen root ordinal", proposalID)
		}
		if manifestGroupKey != "legacy" &&
			(proposalManifest.DeliverySliceID == nil || *proposalManifest.DeliverySliceID != buildManifest.SliceIDs[proposalIndex]) {
			return "", fmt.Errorf("implementation proposal %s has the wrong frozen delivery slice", proposalID)
		}
		rootID := proposalManifest.RootManifestID
		if rootID == uuid.Nil {
			rootID = proposalManifest.ID
		}
		if rootID != allowedRootOrder[proposalIndex] || !allowedRoots[rootID] || coveredRoots[rootID] || proposal.AppliedAt == nil || (proposal.Status != "applied" && proposal.Status != "partially_applied") {
			return "", fmt.Errorf("implementation proposal %s is not an applied output of the frozen build manifest", proposalID)
		}
		if err := validateBuildManifestRootLineage(ctx, v.Database, proposalManifest, rootID, proposal.ProjectID, workflowRunID); err != nil {
			return "", fmt.Errorf("implementation proposal %s has invalid build manifest lineage: %w", proposalID, err)
		}
		coveredRoots[rootID] = true
	}
	for rootID := range allowedRoots {
		var appliedProposalRows []struct {
			ID uuid.UUID
		}
		if err := v.Database.WithContext(ctx).Table("implementation_proposals AS proposals").
			Select("proposals.id").
			Joins("JOIN application_build_manifests AS manifests ON manifests.id = proposals.build_manifest_id").
			Where(
				"proposals.project_id = ? AND manifests.root_manifest_id = ? AND manifests.workflow_run_id = ? AND proposals.status IN ? AND proposals.applied_at IS NOT NULL",
				execution.Run.ProjectID, rootID, workflowRunID, []string{"applied", "partially_applied"},
			).Scan(&appliedProposalRows).Error; err != nil {
			return "", err
		}
		if len(appliedProposalRows) != 1 || !seen[appliedProposalRows[0].ID.String()] {
			return "", fmt.Errorf("build manifest root %s must have exactly one selected applied proposal", rootID)
		}
	}
	var workspace storage.ArtifactRevisionModel
	err = v.Database.WithContext(ctx).Table("artifact_revisions").
		Select("artifact_revisions.*").
		Joins("JOIN artifacts ON artifacts.id = artifact_revisions.artifact_id").
		Where("artifact_revisions.id = ? AND artifact_revisions.artifact_id = ? AND artifact_revisions.content_hash = ? AND artifact_revisions.workflow_status = 'approved' AND artifacts.project_id = ? AND artifacts.kind = 'workspace' AND artifacts.latest_approved_revision_id = artifact_revisions.id", envelope.WorkspaceRevision.RevisionID, envelope.WorkspaceRevision.ArtifactID, envelope.WorkspaceRevision.ContentHash, execution.Run.ProjectID).
		Take(&workspace).Error
	if err != nil {
		return "", err
	}
	selectedDescendantOrder := make([]string, 0, len(seen))
	visitedRevisions := map[uuid.UUID]bool{}
	current := workspace
	for current.ID != uuid.Nil {
		if visitedRevisions[current.ID] {
			return "", fmt.Errorf("workspace revision lineage contains a cycle")
		}
		visitedRevisions[current.ID] = true
		if current.ImplementationProposalID != nil {
			proposalID := current.ImplementationProposalID.String()
			if seen[proposalID] {
				selectedDescendantOrder = append(selectedDescendantOrder, proposalID)
			}
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
	if err := validateSelectedProposalOrder(envelope.ImplementationProposalIDs, selectedDescendantOrder); err != nil {
		return "", err
	}
	return workspace.ID.String(), nil
}

func validateSelectedProposalOrder(expectedAncestorOrder, actualDescendantOrder []string) error {
	if len(actualDescendantOrder) != len(expectedAncestorOrder) {
		return fmt.Errorf("workspace revision lineage does not contain every selected implementation proposal")
	}
	for index, proposalID := range expectedAncestorOrder {
		actual := actualDescendantOrder[len(actualDescendantOrder)-1-index]
		if actual != proposalID {
			return fmt.Errorf(
				"workspace implementation order does not match frozen bundle order at position %d", index,
			)
		}
	}
	return nil
}

func validateBuildManifestRootLineage(
	ctx context.Context,
	database *gorm.DB,
	manifest storage.ApplicationBuildManifestModel,
	rootID uuid.UUID,
	projectID uuid.UUID,
	workflowRunID uuid.UUID,
) error {
	visited := map[uuid.UUID]bool{}
	current := manifest
	expectedManifestGroup := manifest.ManifestGroupKey
	expectedDeliverySliceID := manifest.DeliverySliceID
	if expectedManifestGroup == nil || strings.TrimSpace(*expectedManifestGroup) == "" {
		return fmt.Errorf("build manifest lineage has no manifest compiler group")
	}
	for {
		if current.ID == uuid.Nil || current.ProjectID != projectID || current.WorkflowRunID == nil ||
			*current.WorkflowRunID != workflowRunID || !optionalWorkflowStringEqual(current.ManifestGroupKey, expectedManifestGroup) ||
			!optionalWorkflowStringEqual(current.DeliverySliceID, expectedDeliverySliceID) ||
			visited[current.ID] {
			return fmt.Errorf("build manifest lineage is invalid or cyclic")
		}
		visited[current.ID] = true
		currentRootID := current.RootManifestID
		if currentRootID == uuid.Nil {
			currentRootID = current.ID
		}
		if currentRootID != rootID {
			return fmt.Errorf("build manifest changed root lineage")
		}
		if current.ID == rootID {
			if current.DerivedFromID != nil {
				return fmt.Errorf("root build manifest has a parent")
			}
			return nil
		}
		if current.DerivedFromID == nil || len(visited) > 10_000 {
			return fmt.Errorf("derived build manifest does not reach its root")
		}
		var parent storage.ApplicationBuildManifestModel
		if err := database.WithContext(ctx).
			Where(
				"id = ? AND project_id = ? AND root_manifest_id = ? AND workflow_run_id = ?",
				*current.DerivedFromID, projectID, rootID, workflowRunID,
			).
			Take(&parent).Error; err != nil {
			return err
		}
		current = parent
	}
}

func optionalWorkflowStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
	key = input.ArtifactKey
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
	if err := validateTargetIdentity(kind, versioned, input); err != nil {
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
	if err := validateCleanReusableDraft(kind, versioned.Draft, versioned.LatestRevision); err != nil {
		return nil, err
	}
	return &core.VersionRef{ArtifactID: selected.ID, RevisionID: versioned.LatestRevision.ID, ContentHash: versioned.LatestRevision.ContentHash}, nil
}

func validateCleanReusableDraft(
	kind string,
	draft *core.ArtifactDraft,
	latest *core.ArtifactRevision,
) error {
	if draft == nil {
		return nil
	}
	if latest == nil || draft.BaseRevisionID == nil || *draft.BaseRevisionID != latest.ID {
		return fmt.Errorf("existing %s target has uncheckpointed draft base lineage", kind)
	}
	if draft.Status != "draft" {
		return fmt.Errorf("existing %s target has uncheckpointed draft status %q", kind, draft.Status)
	}
	if draft.SchemaVersion != latest.SchemaVersion {
		return fmt.Errorf("existing %s target has uncheckpointed draft schema", kind)
	}
	if draft.ContentHash != latest.ContentHash {
		return fmt.Errorf("existing %s target has uncheckpointed draft content", kind)
	}
	if len(draft.SourceVersions) != len(latest.SourceVersions) {
		return fmt.Errorf("existing %s target has uncheckpointed draft source lineage", kind)
	}
	for index := range draft.SourceVersions {
		left, right := draft.SourceVersions[index], latest.SourceVersions[index]
		if left.Purpose != right.Purpose || left.Required != right.Required ||
			!sameExactCoreVersionRef(left.VersionRef, right.VersionRef) {
			return fmt.Errorf("existing %s target has uncheckpointed draft source lineage at index %d", kind, index)
		}
	}
	return nil
}

func sameExactCoreVersionRef(left, right core.VersionRef) bool {
	if left.ArtifactID != right.ArtifactID || left.RevisionID != right.RevisionID || left.ContentHash != right.ContentHash {
		return false
	}
	if left.AnchorID == nil || right.AnchorID == nil {
		return left.AnchorID == nil && right.AnchorID == nil
	}
	return *left.AnchorID == *right.AnchorID
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
		input.ArtifactKey += "-" + stableArtifactIdentitySuffix(source.Ref.ArtifactID) + "-" + stableArtifactIdentitySuffix(pageNodeID)
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
		input.ArtifactKey += "-" + stableArtifactIdentitySuffix(source.Ref.ArtifactID)
		input.SourceVersions = []core.ArtifactSourceInput{{
			Ref: source.Ref, Purpose: "page_spec", Required: true,
		}}
	}
	return input, nil
}

func stableArtifactIdentitySuffix(artifactID string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(artifactID)))
	return strings.ToUpper(fmt.Sprintf("%x", hash[:5]))
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

func validateTargetIdentity(
	kind string,
	artifact core.VersionedArtifact,
	desired core.CreateArtifactInput,
) error {
	if kind != "blueprint" && kind != "page_spec" && kind != "prototype" {
		return nil
	}
	if artifact.Artifact.ID == "" || artifact.Artifact.Kind != kind || artifact.Artifact.ArtifactKey != desired.ArtifactKey {
		return fmt.Errorf("existing %s target does not match its stable artifact identity", kind)
	}
	if artifact.LatestRevision == nil {
		if artifact.Draft == nil {
			return fmt.Errorf("existing %s target has no exact draft or revision lineage", kind)
		}
		return validateInitialTargetLineage(kind, artifact.Draft.Content, artifact.Draft.SourceVersions, desired)
	}
	if artifact.Artifact.LatestRevisionID == nil || *artifact.Artifact.LatestRevisionID != artifact.LatestRevision.ID ||
		artifact.LatestRevision.ArtifactID != artifact.Artifact.ID || artifact.LatestRevision.ContentHash == "" {
		return fmt.Errorf("existing %s target latest revision identity is invalid", kind)
	}
	return validateReusableTargetLineage(kind, artifact.LatestRevision.Content, artifact.LatestRevision.SourceVersions, desired)
}

func validateInitialTargetLineage(
	kind string,
	content json.RawMessage,
	sources []core.ArtifactSource,
	desired core.CreateArtifactInput,
) error {
	if len(sources) != len(desired.SourceVersions) {
		return fmt.Errorf("initial %s target lineage differs from the immutable workflow input", kind)
	}
	for index, expected := range desired.SourceVersions {
		actual := sources[index]
		if actual.Purpose != expected.Purpose || actual.Required != expected.Required ||
			!sameCoreVersionRef(actual.VersionRef, expected.Ref) {
			return fmt.Errorf("initial %s target source %d differs from the immutable workflow input", kind, index)
		}
	}
	if !sameTargetLogicalContent(kind, content, sources, desired, true) {
		return fmt.Errorf("initial %s target content differs from the immutable workflow input", kind)
	}
	return nil
}

func validateReusableTargetLineage(
	kind string,
	content json.RawMessage,
	sources []core.ArtifactSource,
	desired core.CreateArtifactInput,
) error {
	if !sameTargetLogicalContent(kind, content, sources, desired, false) {
		return fmt.Errorf("existing %s target belongs to another logical source artifact or page", kind)
	}
	return nil
}

func sameTargetLogicalContent(
	kind string,
	content json.RawMessage,
	sources []core.ArtifactSource,
	desired core.CreateArtifactInput,
	exact bool,
) bool {
	if kind == "blueprint" {
		return json.Valid(content)
	}
	if len(sources) != 1 || len(desired.SourceVersions) != 1 {
		return false
	}
	actualSource, desiredSource := sources[0], desired.SourceVersions[0]
	if actualSource.Purpose != desiredSource.Purpose || actualSource.Required != desiredSource.Required ||
		actualSource.ArtifactID != desiredSource.Ref.ArtifactID {
		return false
	}
	if exact && !sameCoreVersionRef(actualSource.VersionRef, desiredSource.Ref) {
		return false
	}
	switch kind {
	case "page_spec":
		actualAnchor, desiredAnchor := coreVersionAnchor(actualSource.VersionRef), coreVersionAnchor(desiredSource.Ref)
		if actualAnchor == "" || actualAnchor != desiredAnchor {
			return false
		}
		var actual, expected struct {
			BlueprintPageNodeID string `json:"blueprintPageNodeId"`
		}
		return json.Unmarshal(content, &actual) == nil && json.Unmarshal(desired.Content, &expected) == nil &&
			strings.TrimSpace(actual.BlueprintPageNodeID) == strings.TrimSpace(expected.BlueprintPageNodeID) &&
			strings.TrimSpace(actual.BlueprintPageNodeID) == actualAnchor
	case "prototype":
		if coreVersionAnchor(actualSource.VersionRef) != "" || coreVersionAnchor(desiredSource.Ref) != "" {
			return false
		}
		var actual, expected struct {
			PageSpecRevision core.VersionRef `json:"pageSpecRevision"`
			Exploratory      bool            `json:"exploratory"`
		}
		if json.Unmarshal(content, &actual) != nil || json.Unmarshal(desired.Content, &expected) != nil {
			return false
		}
		if exact {
			return actual.Exploratory == expected.Exploratory &&
				sameCoreVersionRef(actual.PageSpecRevision, expected.PageSpecRevision)
		}
		return actual.PageSpecRevision.ArtifactID == expected.PageSpecRevision.ArtifactID &&
			actual.PageSpecRevision.ArtifactID == desiredSource.Ref.ArtifactID
	default:
		return false
	}
}

func coreVersionAnchor(reference core.VersionRef) string {
	if reference.AnchorID == nil {
		return ""
	}
	return strings.TrimSpace(*reference.AnchorID)
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
		if _, exists := execution.Run.Context.Slices[currentSliceID]; !exists {
			return domain.InputManifest{}, fmt.Errorf("current delivery slice %q is missing", currentSliceID)
		}
		pinned := make([]domain.WorkflowSliceRef, 0, 1)
		for _, ref := range execution.Inputs.SliceRefs() {
			if ref.ID == currentSliceID {
				pinned = append(pinned, ref)
			}
		}
		if len(pinned) != 1 {
			return domain.InputManifest{}, fmt.Errorf("current delivery slice %q requires exactly one immutable input pin, got %d", currentSliceID, len(pinned))
		}
		for purpose, ref := range map[string]*domain.ArtifactRef{"delivery_slice_blueprint": pinned[0].Blueprint, "delivery_slice_page_spec": pinned[0].PageSpec, "delivery_slice_prototype": pinned[0].Prototype} {
			if ref != nil && ref.Validate() == nil {
				sourceByArtifact[ref.ArtifactID] = core.ManifestSourceInput{Ref: toCoreVersionRef(*ref), Purpose: purpose}
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
	if jobType == "refine_project_brief" {
		if upstream.BaseRevision == nil {
			return domain.InputManifest{}, fmt.Errorf("refine_project_brief requires the stable entry Project Brief base")
		}
		current := make([]domain.ArtifactRef, 0, 1)
		for _, ref := range execution.Inputs.ArtifactRefs() {
			if ref.ArtifactID != upstream.BaseRevision.ArtifactID || ref.AnchorID != "" {
				continue
			}
			duplicate := false
			for _, existing := range current {
				if sameArtifactRevision(existing, ref) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				current = append(current, ref)
			}
		}
		if len(current) != 1 {
			return domain.InputManifest{}, fmt.Errorf("refine_project_brief requires exactly one current whole Project Brief revision, got %d", len(current))
		}
		converted := toCoreVersionRef(current[0])
		base = &converted
	} else if a.Targets != nil {
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
	GetLineageState(context.Context, string, string) (core.WorkbenchLineageState, error)
}

type CoreWorkbenchManifestHook struct {
	Workbench CoreWorkbenchAPI
	Proposals CoreProposalAPI
	Now       func() time.Time
}

func (h CoreWorkbenchManifestHook) Compile(ctx context.Context, execution Execution) (BuildManifest, error) {
	if h.Workbench == nil || h.Proposals == nil || execution.Run.InputManifest == nil {
		return BuildManifest{}, fmt.Errorf("workbench, proposal service and run input manifest are required")
	}
	if execution.Definition.ManifestCompiler == nil {
		return BuildManifest{}, fmt.Errorf("manifest compiler node config is required")
	}
	config := execution.Definition.ManifestCompiler
	if config.ManifestKind != "application_build" || config.SchemaVersion != 1 || config.Hook != "application-build-manifest/v1" {
		return BuildManifest{}, fmt.Errorf("unsupported manifest compiler %s/%d/%s", config.ManifestKind, config.SchemaVersion, config.Hook)
	}
	upstream, err := h.Proposals.GetManifest(ctx, execution.Run.InputManifest.ID, execution.Run.StartedBy)
	if err != nil {
		return BuildManifest{}, err
	}
	if upstream.Ref() != *execution.Run.InputManifest || upstream.ProjectID != execution.Run.ProjectID {
		return BuildManifest{}, fmt.Errorf("workflow run input manifest drifted")
	}
	if execution.Workflow.OutputContract == nil || execution.Workflow.OutputContract.Validate() != nil {
		return BuildManifest{}, fmt.Errorf("application build requires a valid workflow output contract")
	}
	runScope := execution.Run.Scope
	if len(runScope) == 0 {
		runScope = json.RawMessage(`{}`)
	}
	runScope, err = domain.CanonicalJSON(runScope)
	if err != nil {
		return BuildManifest{}, fmt.Errorf("canonicalize workflow run scope: %w", err)
	}
	sliceRefs := execution.Inputs.SliceRefs()
	mergedFanOutID := ""
	for _, ref := range sliceRefs {
		if mergedFanOutID == "" {
			mergedFanOutID = ref.FanOutNodeID
			continue
		}
		if ref.FanOutNodeID != mergedFanOutID {
			return BuildManifest{}, fmt.Errorf("build manifest input combines multiple delivery fan-out epochs")
		}
	}
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
	selection, selected, err := blueprintSelectionScope(upstream)
	if err != nil {
		return BuildManifest{}, err
	}
	if selected {
		if err := validateBlueprintSelectionSlices(selection, slices); err != nil {
			return BuildManifest{}, err
		}
	}
	bundleIDs := make([]string, 0, len(slices))
	sliceIDs := make([]string, 0, len(slices))
	sources := make([]domain.ArtifactRef, 0)
	manifestGroupKey := execution.Node.ID
	if _, err := uuid.Parse(manifestGroupKey); err != nil {
		return BuildManifest{}, fmt.Errorf("manifest compiler node run id is invalid")
	}
	for rootOrdinal, slice := range slices {
		if slice.Prototype == nil {
			return BuildManifest{}, fmt.Errorf("slice %s has no exact prototype revision", slice.Key)
		}
		if err := slice.Prototype.Validate(); err != nil {
			return BuildManifest{}, err
		}
		runID, sliceID := execution.Run.ID, slice.ID
		ordinal := rootOrdinal
		outputContract := *execution.Workflow.OutputContract
		outputContract.ProducedArtifactKinds = append([]string(nil), execution.Workflow.OutputContract.ProducedArtifactKinds...)
		createInput := core.CreateWorkbenchBundleInput{
			PrototypeRevision: toCoreVersionRef(*slice.Prototype), WorkflowRunID: &runID,
			ManifestGroupKey: &manifestGroupKey, RootOrdinal: &ordinal, DeliverySliceID: &sliceID,
		}
		buildContext := core.ApplicationBuildContext{
			Definition: execution.Run.Definition, ExecutionProfile: execution.Run.ExecutionProfile, InputManifest: upstream, DeliverySliceID: slice.ID,
			RunScope: runScope, OutputContract: &outputContract,
		}
		bundleInput := core.NewWorkflowWorkbenchBundleInput(createInput, buildContext)
		if execution.legacyProfileView {
			bundleInput = core.NewLegacyWorkflowWorkbenchBundleInput(createInput, buildContext)
		}
		bundle, err := h.Workbench.CreateBundle(ctx, execution.Run.ProjectID, execution.Run.StartedBy, bundleInput)
		if err != nil {
			return BuildManifest{}, err
		}
		bundleIDs = append(bundleIDs, bundle.ID)
		sliceIDs = append(sliceIDs, slice.ID)
		for _, ref := range refsFromBundle(bundle) {
			sources = appendUniqueArtifactRef(sources, ref)
		}
	}
	for _, source := range upstream.Sources {
		sources = appendUniqueArtifactRef(sources, source.Ref)
	}
	if upstream.BaseRevision != nil {
		sources = appendUniqueArtifactRef(sources, *upstream.BaseRevision)
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	constraints, _ := domain.CanonicalJSON(map[string]any{"definition": execution.Run.Definition, "scope": json.RawMessage(execution.Run.Scope)})
	return BuildManifest{SchemaVersion: execution.Definition.ManifestCompiler.SchemaVersion, ProjectID: execution.Run.ProjectID, RunID: execution.Run.ID, ManifestGroupKey: manifestGroupKey, SliceIDs: sliceIDs, BundleIDs: bundleIDs, Sources: sources, Constraints: constraints, CreatedAt: now}, nil
}

func validateBlueprintSelectionSlices(selection workflowBlueprintSelectionScope, slices []SliceContext) error {
	bindings := make(map[string]workflowBlueprintSelectionPageBinding, len(selection.PageBindings))
	for _, binding := range selection.PageBindings {
		if binding.PageSpec == nil || binding.Prototype == nil {
			return fmt.Errorf("selected Blueprint page %s is missing an approved PageSpec or Prototype", binding.NodeID)
		}
		bindings[binding.NodeID] = binding
	}
	if len(bindings) == 0 || len(slices) != len(bindings) {
		return fmt.Errorf("build slices do not exactly match the frozen Blueprint selection")
	}
	seen := map[string]bool{}
	for _, slice := range slices {
		nodeID := slice.Blueprint.AnchorID
		binding, exists := bindings[nodeID]
		if !exists || seen[nodeID] {
			return fmt.Errorf("build slice %s is outside the frozen Blueprint selection", slice.Key)
		}
		seen[nodeID] = true
		if slice.Blueprint.ArtifactID != selection.Blueprint.ArtifactID ||
			slice.Blueprint.RevisionID != selection.Blueprint.RevisionID ||
			slice.Blueprint.ContentHash != selection.Blueprint.ContentHash ||
			slice.PageSpec == nil || !slice.PageSpec.Equal(*binding.PageSpec) ||
			slice.Prototype == nil || !slice.Prototype.Equal(*binding.Prototype) {
			return fmt.Errorf("build slice %s lineage differs from its frozen Blueprint selection binding", slice.Key)
		}
	}
	return nil
}

type ImplementationGenerator interface {
	GenerateImplementation(context.Context, generation.ImplementationGenerationRequest) (generation.ImplementationGenerationResult, error)
}

type GenerationWorkbenchRunner struct {
	Generation   ImplementationGenerator
	Workbench    CoreWorkbenchAPI
	DefaultModel string
	Instruction  string
}

func (r GenerationWorkbenchRunner) Run(ctx context.Context, execution Execution) (WorkerResult, error) {
	if r.Generation == nil || r.Workbench == nil {
		return WorkerResult{}, fmt.Errorf("generation and workbench lineage services are required")
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
	for rootIndex, rootBundleID := range manifest.BundleIDs {
		state, err := r.Workbench.GetLineageState(ctx, rootBundleID, execution.Run.StartedBy)
		if err != nil {
			return WorkerResult{}, err
		}
		if state.RootBundleID != rootBundleID || state.ActiveBundle.ID == "" ||
			(state.ActiveBundle.RootBuildManifestID != "" && state.ActiveBundle.RootBuildManifestID != rootBundleID) {
			return WorkerResult{}, fmt.Errorf("workbench lineage state does not match frozen root %s", rootBundleID)
		}
		if err := validateBuildManifestBundleSourceCoverage(manifest, state.ActiveBundle); err != nil {
			return WorkerResult{}, err
		}
		if state.CurrentProposal != nil &&
			(state.CurrentProposal.Status == "applied" || state.CurrentProposal.Status == "partially_applied") {
			results = append(results, workbenchProposalResult(rootBundleID, state.ActiveBundle.ID, *state.CurrentProposal))
			continue
		}
		if !sameOptionalWorkflowVersionRef(state.ActiveBundle.CurrentWorkspaceRevision, state.CurrentWorkspaceRevision) {
			return workbenchWaitInputResult(results, manifest.BundleIDs[rootIndex:]), nil
		}
		if state.CurrentProposal != nil {
			results = append(results, workbenchProposalResult(rootBundleID, state.ActiveBundle.ID, *state.CurrentProposal))
			return workbenchWaitInputResult(results, manifest.BundleIDs[rootIndex+1:]), nil
		}
		generated, err := r.Generation.GenerateImplementation(ctx, generation.ImplementationGenerationRequest{
			BundleID: state.ActiveBundle.ID, ActorID: execution.Run.StartedBy, Model: r.DefaultModel,
			Instruction:     instruction,
			ExecutionSource: core.ImplementationSourceWorkflowRunner,
			ExpectedRunID:   execution.Run.ID, ExpectedRootBundleID: rootBundleID,
		})
		if err != nil {
			return WorkerResult{}, err
		}
		if generated.Proposal.Status == "applied" || generated.Proposal.Status == "partially_applied" {
			return WorkerResult{}, fmt.Errorf("generation returned an already-applied proposal")
		}
		results = append(results, workbenchProposalResult(rootBundleID, state.ActiveBundle.ID, generated.Proposal))
		return workbenchWaitInputResult(results, manifest.BundleIDs[rootIndex+1:]), nil
	}
	return workbenchWaitInputResult(results, nil), nil
}

func workbenchProposalResult(rootBundleID, activeBundleID string, proposal core.ImplementationProposal) map[string]any {
	return map[string]any{
		"bundleId": rootBundleID, "activeBundleId": activeBundleID,
		"proposalId": proposal.ID, "payloadHash": proposal.PayloadHash, "status": proposal.Status,
	}
}

func workbenchWaitInputResult(results []map[string]any, pending []string) WorkerResult {
	return WorkerResult{Disposition: ResultWaitInput, Output: mustJSON(map[string]any{
		"implementationProposals": results,
		"pendingBundleIds":        append([]string(nil), pending...),
	})}
}

func sameOptionalWorkflowVersionRef(left, right *core.VersionRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftAnchor, rightAnchor := "", ""
	if left.AnchorID != nil {
		leftAnchor = *left.AnchorID
	}
	if right.AnchorID != nil {
		rightAnchor = *right.AnchorID
	}
	return left.ArtifactID == right.ArtifactID && left.RevisionID == right.RevisionID &&
		left.ContentHash == right.ContentHash && leftAnchor == rightAnchor
}

func workbenchInstruction(scope json.RawMessage, fallback string) (generation.ImplementationInstruction, error) {
	if len(scope) == 0 {
		return generation.ImplementationInstruction{Objective: strings.TrimSpace(fallback)}, nil
	}
	var envelope struct {
		ConversationIntent struct {
			WorkbenchInstruction json.RawMessage `json:"workbenchInstruction"`
		} `json:"conversationIntent"`
	}
	if err := json.Unmarshal(scope, &envelope); err != nil {
		return generation.ImplementationInstruction{}, fmt.Errorf("decode workbench instruction from run scope: %w", err)
	}
	if len(envelope.ConversationIntent.WorkbenchInstruction) == 0 || string(envelope.ConversationIntent.WorkbenchInstruction) == "null" {
		return generation.ImplementationInstruction{Objective: strings.TrimSpace(fallback)}, nil
	}
	var instruction struct {
		Objective   string   `json:"objective"`
		Constraints []string `json:"constraints,omitempty"`
	}
	if err := json.Unmarshal(envelope.ConversationIntent.WorkbenchInstruction, &instruction); err != nil {
		return generation.ImplementationInstruction{}, fmt.Errorf("decode structured workbench instruction: %w", err)
	}
	if strings.TrimSpace(instruction.Objective) == "" {
		return generation.ImplementationInstruction{}, fmt.Errorf("conversation workbench instruction objective is required")
	}
	normalized, _, _, err := generation.CanonicalImplementationInstruction(instruction.Objective, instruction.Constraints)
	if err != nil {
		return generation.ImplementationInstruction{}, err
	}
	return normalized, nil
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
	BlueprintPages FanOutResolver
}

func (r DefinitionFanOutResolver) Resolve(ctx context.Context, execution Execution) ([]FanOutItem, error) {
	config := execution.Definition.FanOut
	if config == nil {
		return nil, fmt.Errorf("fan-out node config is required")
	}
	var items []FanOutItem
	var err error
	if config.ItemKind == "delivery_slice" && r.DeliverySlices != nil {
		items, err = r.DeliverySlices.Resolve(ctx, execution)
	} else if config.ItemKind == "blueprint_page" || config.ItemKind == "blueprint_selection_page" {
		if r.BlueprintPages == nil {
			return nil, fmt.Errorf("blueprint_page fan-out resolver is required")
		}
		items, err = r.BlueprintPages.Resolve(ctx, execution)
	} else {
		items, err = (InputEnvelopeFanOutResolver{}).Resolve(ctx, execution)
	}
	if err != nil {
		return nil, err
	}
	limit, err := effectiveFanOutMaxItems(config)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		return nil, fmt.Errorf("fan-out resolver produced %d items, exceeding maxItems %d", len(items), limit)
	}
	return items, nil
}

// CoreBlueprintPageFanOutResolver derives branches only from the immutable
// Blueprint ref carried by the incoming edge binding. It never trusts a client
// supplied page array or mutable run-context value.
type CoreBlueprintPageFanOutResolver struct {
	Artifacts HumanEditArtifactAPI
	Proposals CoreProposalAPI
}

func (r CoreBlueprintPageFanOutResolver) Resolve(
	ctx context.Context,
	execution Execution,
) ([]FanOutItem, error) {
	if r.Artifacts == nil {
		return nil, fmt.Errorf("Blueprint artifact service is required")
	}
	selection, selected, err := loadRunBlueprintSelection(ctx, r.Proposals, execution)
	if err != nil {
		return nil, err
	}
	if execution.Definition.FanOut != nil && execution.Definition.FanOut.ItemKind == "blueprint_selection_page" && !selected {
		return nil, fmt.Errorf("blueprint_selection_page fan-out requires a frozen Blueprint selection manifest")
	}
	refs := make([]domain.ArtifactRef, 0, 1)
	seen := map[string]struct{}{}
	for _, binding := range execution.Inputs.Bindings() {
		if binding.Source.RunID != execution.Run.ID {
			return nil, fmt.Errorf("blueprint_page fan-out input belongs to another workflow run")
		}
		for _, ref := range binding.Source.ArtifactRevisions {
			if ref.AnchorID != "" {
				continue
			}
			artifact, err := r.Artifacts.Get(ctx, ref.ArtifactID, execution.Run.StartedBy, false)
			if err != nil {
				return nil, fmt.Errorf("resolve fan-out source artifact %s: %w", ref.ArtifactID, err)
			}
			if artifact.Artifact.ProjectID != execution.Run.ProjectID {
				return nil, fmt.Errorf("fan-out source artifact belongs to another project")
			}
			if artifact.Artifact.Kind != "blueprint" {
				continue
			}
			if !selected {
				if err := validateCurrentApprovedBlueprintRef(artifact, ref); err != nil {
					return nil, err
				}
			} else if !ref.Equal(selection.Blueprint) {
				return nil, fmt.Errorf("blueprint_page fan-out source differs from frozen selection Blueprint")
			}
			key := ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, ref)
		}
	}
	if len(refs) != 1 {
		return nil, fmt.Errorf("blueprint_page fan-out requires exactly one current approved whole Blueprint revision, got %d", len(refs))
	}
	blueprint := refs[0]
	revision, err := r.Artifacts.GetRevision(ctx, blueprint.RevisionID, execution.Run.StartedBy)
	if err != nil {
		return nil, fmt.Errorf("load Blueprint revision content: %w", err)
	}
	if revision.ID != blueprint.RevisionID || revision.ArtifactID != blueprint.ArtifactID || revision.ContentHash != blueprint.ContentHash || revision.WorkflowStatus != "approved" {
		return nil, fmt.Errorf("Blueprint revision id, artifact, hash, and approved status must match exactly")
	}
	pages, err := semanticBlueprintPages(revision.Content)
	if err != nil {
		return nil, err
	}
	selectedNodes := map[string]bool{}
	bindings := map[string]workflowBlueprintSelectionPageBinding{}
	if selected {
		for _, nodeID := range selection.NodeIDs {
			selectedNodes[nodeID] = true
		}
		for _, binding := range selection.PageBindings {
			bindings[binding.NodeID] = binding
		}
	}
	items := make([]FanOutItem, 0, len(pages))
	seenKeys := map[string]struct{}{}
	for _, page := range pages {
		if selected && !selectedNodes[page.ID] {
			continue
		}
		if _, duplicate := seenKeys[page.Key]; duplicate {
			return nil, fmt.Errorf("Blueprint Page keys must be unique")
		}
		seenKeys[page.Key] = struct{}{}
		anchored := blueprint
		anchored.AnchorID = page.ID
		payload, err := domain.CanonicalJSON(map[string]any{
			"key": page.Key, "title": page.Title, "pageNodeId": page.ID,
			"route": page.Route, "userGoal": page.UserGoal,
			"description": page.Description, "requirementIds": page.RequirementIDs,
			"blueprint": anchored,
		})
		if err != nil {
			return nil, err
		}
		item := FanOutItem{
			Key: page.Key, Title: page.Title, Payload: payload, Blueprint: anchored,
		}
		if selected {
			binding, exists := bindings[page.ID]
			if !exists {
				return nil, fmt.Errorf("selected Blueprint page %s has no frozen binding", page.ID)
			}
			item.PageSpec, item.Prototype = binding.PageSpec, binding.Prototype
		}
		items = append(items, item)
	}
	if selected && len(items) != len(bindings) {
		return nil, fmt.Errorf("Blueprint selection page bindings do not match semantic Page anchors")
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Key == items[right].Key {
			return items[left].Blueprint.AnchorID < items[right].Blueprint.AnchorID
		}
		return items[left].Key < items[right].Key
	})
	return items, nil
}

func validateCurrentApprovedBlueprintRef(artifact core.VersionedArtifact, ref domain.ArtifactRef) error {
	if ref.AnchorID != "" || artifact.Artifact.LatestRevisionID == nil || artifact.Artifact.LatestApprovedRevisionID == nil ||
		*artifact.Artifact.LatestRevisionID != ref.RevisionID || *artifact.Artifact.LatestApprovedRevisionID != ref.RevisionID ||
		artifact.Artifact.SyncStatus != "current" {
		return fmt.Errorf("blueprint_page fan-out source is not the current approved whole Blueprint revision")
	}
	return nil
}

type semanticBlueprintPage struct {
	ID             string
	Key            string
	Title          string
	Description    string
	Route          string
	UserGoal       string
	RequirementIDs []string
}

func semanticBlueprintPages(content json.RawMessage) ([]semanticBlueprintPage, error) {
	decoded, err := core.DecodeBlueprintPages(content)
	if err != nil {
		return nil, err
	}
	pages := make([]semanticBlueprintPage, 0, len(decoded))
	for _, page := range decoded {
		pages = append(pages, semanticBlueprintPage{
			ID: page.ID, Key: page.Key, Title: page.Title, Description: page.Description,
			Route: page.Route, UserGoal: page.UserGoal,
			RequirementIDs: append([]string(nil), page.RequirementIDs...),
		})
	}
	return pages, nil
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
	limit, err := effectiveFanOutMaxItems(e.Definition.FanOut)
	if err != nil {
		return WorkerResult{}, err
	}
	if len(items) > limit {
		return WorkerResult{}, fmt.Errorf("fan-out resolver produced %d items, exceeding maxItems %d", len(items), limit)
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

type QualityManifestResolver interface {
	Resolve(context.Context, Execution, domain.ArtifactRef, []BuildManifest) (BuildManifest, error)
}

type QualityManifestResolverFunc func(context.Context, Execution, domain.ArtifactRef, []BuildManifest) (BuildManifest, error)

func (f QualityManifestResolverFunc) Resolve(
	ctx context.Context,
	execution Execution,
	workspace domain.ArtifactRef,
	manifests []BuildManifest,
) (BuildManifest, error) {
	return f(ctx, execution, workspace, manifests)
}

// CoreQualityManifestResolver maps the exact final WorkspaceRevision back to
// the implementation proposal and manifest leaf that produced it. This lets a
// quality node consume several compiler/workbench ancestors while selecting
// one unambiguous producer group for publish provenance.
type CoreQualityManifestResolver struct{ Database *gorm.DB }

func (r CoreQualityManifestResolver) Resolve(
	ctx context.Context,
	execution Execution,
	workspace domain.ArtifactRef,
	manifests []BuildManifest,
) (BuildManifest, error) {
	if r.Database == nil {
		return BuildManifest{}, fmt.Errorf("quality manifest database is required")
	}
	if err := workspace.Validate(); err != nil || strings.TrimSpace(workspace.AnchorID) != "" {
		return BuildManifest{}, fmt.Errorf("quality workspace revision is invalid")
	}
	if len(manifests) == 0 {
		return BuildManifest{}, fmt.Errorf("quality has no incoming frozen application build manifest")
	}
	projectID, err := uuid.Parse(execution.Run.ProjectID)
	if err != nil {
		return BuildManifest{}, fmt.Errorf("quality workflow project id is invalid")
	}
	runID, err := uuid.Parse(execution.Run.ID)
	if err != nil {
		return BuildManifest{}, fmt.Errorf("quality workflow run id is invalid")
	}
	artifactID, err := uuid.Parse(workspace.ArtifactID)
	if err != nil {
		return BuildManifest{}, fmt.Errorf("quality workspace artifact id is invalid")
	}
	revisionID, err := uuid.Parse(workspace.RevisionID)
	if err != nil {
		return BuildManifest{}, fmt.Errorf("quality workspace revision id is invalid")
	}

	var revision storage.ArtifactRevisionModel
	if err := r.Database.WithContext(ctx).Table("artifact_revisions AS revision").
		Select("revision.*").
		Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Where(
			"revision.id = ? AND revision.artifact_id = ? AND revision.content_hash = ? AND revision.workflow_status = ? AND revision.implementation_proposal_id IS NOT NULL AND artifact.project_id = ? AND artifact.kind = ? AND artifact.lifecycle = ? AND artifact.latest_approved_revision_id = revision.id",
			revisionID, artifactID, workspace.ContentHash, "approved", projectID, "workspace", "active",
		).Take(&revision).Error; err != nil {
		return BuildManifest{}, fmt.Errorf("quality workspace is not the exact current implementation output: %w", err)
	}

	var proposal storage.ImplementationProposalModel
	if err := r.Database.WithContext(ctx).Where(
		"id = ? AND project_id = ? AND status IN ? AND applied_at IS NOT NULL AND applied_by IS NOT NULL",
		*revision.ImplementationProposalID, projectID, []string{"applied", "partially_applied"},
	).Take(&proposal).Error; err != nil {
		return BuildManifest{}, fmt.Errorf("quality workspace producer proposal is not applied: %w", err)
	}
	var producer storage.ApplicationBuildManifestModel
	if err := r.Database.WithContext(ctx).Where(
		"id = ? AND project_id = ? AND workflow_run_id = ? AND status = ?",
		proposal.BuildManifestID, projectID, runID, "consumed",
	).Take(&producer).Error; err != nil {
		return BuildManifest{}, fmt.Errorf("quality workspace producer manifest is unavailable: %w", err)
	}
	rootID := producer.RootManifestID
	if rootID == uuid.Nil {
		rootID = producer.ID
	}
	if producer.ManifestGroupKey == nil || strings.TrimSpace(*producer.ManifestGroupKey) == "" || producer.RootOrdinal == nil {
		return BuildManifest{}, fmt.Errorf("quality workspace producer has no compiler coordinate")
	}
	if err := validateBuildManifestRootLineage(ctx, r.Database, producer, rootID, projectID, runID); err != nil {
		return BuildManifest{}, fmt.Errorf("quality workspace producer lineage is invalid: %w", err)
	}

	matches := make([]BuildManifest, 0, 1)
	for _, manifest := range manifests {
		if err := manifest.Validate(); err != nil || manifest.ProjectID != execution.Run.ProjectID || manifest.RunID != execution.Run.ID || manifest.ManifestGroupKey != *producer.ManifestGroupKey {
			continue
		}
		for ordinal, bundleID := range manifest.BundleIDs {
			if bundleID == rootID.String() && ordinal == *producer.RootOrdinal && ordinal == len(manifest.BundleIDs)-1 &&
				(producer.ManifestGroupKey != nil && *producer.ManifestGroupKey == "legacy" ||
					producer.DeliverySliceID != nil && *producer.DeliverySliceID == manifest.SliceIDs[ordinal]) {
				matches = append(matches, manifest)
				break
			}
		}
	}
	if len(matches) != 1 {
		return BuildManifest{}, fmt.Errorf("quality workspace producer maps to %d incoming compiler manifests", len(matches))
	}
	return matches[0], nil
}

type QualityGateRunner struct {
	Evaluator        QualityEvaluator
	ManifestResolver QualityManifestResolver
	Access           PublishAuthorizer
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
	manifests, err := buildManifestsFromInputLineage(e)
	if err != nil {
		return WorkerResult{}, err
	}
	result, err := r.Evaluator.Evaluate(ctx, e)
	if err != nil {
		return WorkerResult{}, err
	}
	if result.WorkspaceRevision == nil {
		return WorkerResult{}, fmt.Errorf("quality evaluator did not return its exact WorkspaceRevision")
	}
	var manifest BuildManifest
	if r.ManifestResolver != nil {
		manifest, err = r.ManifestResolver.Resolve(ctx, e, *result.WorkspaceRevision, manifests)
	} else if len(manifests) == 1 {
		// Direct runner construction in small unit tests retains the historical
		// single-manifest behavior. The platform bootstrap always injects the
		// authoritative resolver above.
		manifest = manifests[0]
	} else {
		err = fmt.Errorf("quality manifest resolver is required for %d compiler groups", len(manifests))
	}
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
	for _, contextRevision := range bundle.ContextRevisions {
		refs = append(refs, fromCoreVersionRef(contextRevision.Revision))
	}
	if bundle.WorkflowContext != nil {
		if bundle.WorkflowContext.InputManifest.BaseRevision != nil {
			refs = append(refs, *bundle.WorkflowContext.InputManifest.BaseRevision)
		}
		for _, source := range bundle.WorkflowContext.InputManifest.Sources {
			refs = append(refs, source.Ref)
		}
	}
	return refs
}

func validateBuildManifestBundleSourceCoverage(manifest BuildManifest, bundle core.WorkbenchBundle) error {
	if bundle.ManifestHash == "" {
		// Runner unit adapters may provide only lineage coordinates. Production
		// CoreWorkbenchAPI always returns a hash-verified frozen bundle.
		return nil
	}
	available := make(map[string]struct{}, len(manifest.Sources))
	for _, ref := range manifest.Sources {
		available[artifactRefIdentity(ref)] = struct{}{}
	}
	for _, required := range refsFromBundle(bundle) {
		if _, exists := available[artifactRefIdentity(required)]; !exists {
			return fmt.Errorf("build manifest sources omit frozen Workbench bundle revision %s/%s", required.ArtifactID, required.RevisionID)
		}
	}
	return nil
}

func artifactRefIdentity(ref domain.ArtifactRef) string {
	return ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" + ref.AnchorID
}
func appendUniqueArtifactRef(refs []domain.ArtifactRef, candidate domain.ArtifactRef) []domain.ArtifactRef {
	for _, ref := range refs {
		if ref.Equal(candidate) {
			return refs
		}
	}
	return append(refs, candidate)
}
