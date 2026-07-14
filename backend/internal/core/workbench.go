package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AssetRef struct {
	AssetID     string `json:"assetId"`
	ContentHash string `json:"contentHash"`
	MediaType   string `json:"mediaType"`
	ByteSize    int64  `json:"byteSize"`
	Name        string `json:"name,omitempty"`
}

type RenderedFrameRef struct {
	AssetRef
	StateID      string `json:"stateId"`
	BreakpointID string `json:"breakpointId"`
}

type WorkbenchBundle struct {
	ID                         string                     `json:"id"`
	ProjectID                  string                     `json:"projectId"`
	RootBuildManifestID        string                     `json:"rootBuildManifestId,omitempty"`
	DerivedFromBuildManifestID *string                    `json:"derivedFromBuildManifestId,omitempty"`
	WorkflowRunID              *string                    `json:"workflowRunId,omitempty"`
	ManifestGroupKey           *string                    `json:"manifestGroupKey,omitempty"`
	DeliverySliceID            *string                    `json:"deliverySliceId,omitempty"`
	PageSpecRevision           VersionRef                 `json:"pageSpecRevision"`
	PrototypeRevision          VersionRef                 `json:"prototypeRevision"`
	RequirementRevisions       []VersionRef               `json:"requirementRevisions"`
	BlueprintRevision          VersionRef                 `json:"blueprintRevision"`
	ContractRevisions          []VersionRef               `json:"contractRevisions"`
	DesignSystemRevisions      []VersionRef               `json:"designSystemRevisions"`
	ContextRevisions           []WorkbenchContextRevision `json:"contextRevisions,omitempty"`
	WorkflowContext            *ApplicationBuildContext   `json:"workflowContext,omitempty"`
	CurrentWorkspaceRevision   *VersionRef                `json:"currentWorkspaceRevision,omitempty"`
	SceneGraph                 AssetRef                   `json:"sceneGraph"`
	RenderedFrames             []RenderedFrameRef         `json:"renderedFrames"`
	InteractionManifest        AssetRef                   `json:"interactionManifest"`
	FixtureBundle              AssetRef                   `json:"fixtureBundle"`
	TokenManifest              AssetRef                   `json:"tokenManifest"`
	ComponentMapping           AssetRef                   `json:"componentMapping"`
	TraceMatrix                AssetRef                   `json:"traceMatrix"`
	AcceptanceManifest         AssetRef                   `json:"acceptanceManifest"`
	Assumptions                []string                   `json:"assumptions"`
	Waivers                    []string                   `json:"waivers"`
	CreatedBy                  string                     `json:"createdBy"`
	CreatedAt                  time.Time                  `json:"createdAt"`
	ManifestHash               string                     `json:"contentHash"`
}

type WorkbenchContextRevision struct {
	Kind     string     `json:"kind"`
	Revision VersionRef `json:"revision"`
}

// ApplicationBuildContext preserves the trusted workflow-level meaning that
// cannot be reconstructed from artifact revisions alone. It is injected only
// by the registered ManifestCompiler adapter and is immutable with the bundle.
type ApplicationBuildContext struct {
	Definition       domain.WorkflowDefinitionRef       `json:"definition"`
	ExecutionProfile domain.WorkflowExecutionProfileRef `json:"executionProfile"`
	InputManifest    domain.InputManifest               `json:"inputManifest"`
	DeliverySliceID  string                             `json:"deliverySliceId,omitempty"`
	RunScope         json.RawMessage                    `json:"runScope,omitempty"`
	OutputContract   *domain.WorkflowOutputContract     `json:"outputContract,omitempty"`
}

func (c ApplicationBuildContext) MarshalJSON() ([]byte, error) {
	type wire ApplicationBuildContext
	encoded, err := json.Marshal(wire(c))
	if err != nil || !c.ExecutionProfile.IsZero() {
		return encoded, err
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, err
	}
	delete(object, "executionProfile")
	return json.Marshal(object)
}

const (
	legacyWorkflowExecutionProfileVersion = "legacy-pre-pin/v0"
	legacyWorkflowExecutionProfileHash    = "bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c"
)

func legacyApplicationBuildContext(value ApplicationBuildContext) bool {
	return value.ExecutionProfile.IsZero() && value.Definition.ExecutionProfile.IsZero()
}

type CreateWorkbenchBundleInput struct {
	PrototypeRevision          VersionRef `json:"prototypeRevision"`
	WorkflowRunID              *string    `json:"workflowRunId,omitempty"`
	ManifestGroupKey           *string    `json:"manifestGroupKey,omitempty"`
	RootOrdinal                *int       `json:"rootOrdinal,omitempty"`
	DeliverySliceID            *string    `json:"deliverySliceId,omitempty"`
	AllowStale                 bool       `json:"allowStale,omitempty"`
	OverrideReason             string     `json:"overrideReason,omitempty"`
	workflowContext            *ApplicationBuildContext
	allowLegacyWorkflowContext bool
}

// NewWorkflowWorkbenchBundleInput is the trusted internal constructor used by
// the workflow ManifestCompiler. workflowContext is deliberately not a JSON
// field on CreateWorkbenchBundleInput, so HTTP clients cannot forge it.
func NewWorkflowWorkbenchBundleInput(
	input CreateWorkbenchBundleInput,
	workflowContext ApplicationBuildContext,
) CreateWorkbenchBundleInput {
	copy := workflowContext
	input.workflowContext = &copy
	return input
}

// NewLegacyWorkflowWorkbenchBundleInput is used only by the exact registered
// pre-pin workflow execution bundle. The compatibility bit is private and the
// service still verifies both persisted rows carry the frozen legacy profile;
// HTTP callers and current-profile runs cannot opt into this path.
func NewLegacyWorkflowWorkbenchBundleInput(
	input CreateWorkbenchBundleInput,
	workflowContext ApplicationBuildContext,
) CreateWorkbenchBundleInput {
	copy := workflowContext
	input.workflowContext = &copy
	input.allowLegacyWorkflowContext = true
	return input
}

// TrustedWorkflowContext returns a defensive copy for internal adapters and
// tests. There is intentionally no public setter.
func (input CreateWorkbenchBundleInput) TrustedWorkflowContext() *ApplicationBuildContext {
	return cloneApplicationBuildContext(input.workflowContext)
}

type RebaseWorkbenchBundleInput struct {
	WorkspaceRevision VersionRef `json:"workspaceRevision"`
}

type WorkbenchLineageProposalSummary struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Version   uint64    `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
}

type WorkbenchLineageEntry struct {
	BundleID                   string                           `json:"bundleId"`
	DerivedFromBuildManifestID *string                          `json:"derivedFromBuildManifestId,omitempty"`
	WorkspaceRevision          *VersionRef                      `json:"workspaceRevision,omitempty"`
	Status                     string                           `json:"status"`
	CreatedAt                  time.Time                        `json:"createdAt"`
	LatestProposal             *WorkbenchLineageProposalSummary `json:"latestProposal,omitempty"`
}

type WorkbenchLineageState struct {
	RootBundleID             string                  `json:"rootBundleId"`
	ActiveBundle             WorkbenchBundle         `json:"activeBundle"`
	CurrentWorkspaceRevision *VersionRef             `json:"currentWorkspaceRevision,omitempty"`
	CurrentProposal          *ImplementationProposal `json:"currentProposal,omitempty"`
	Lineage                  []WorkbenchLineageEntry `json:"lineage"`
}

type WorkbenchService struct {
	database             *gorm.DB
	contents             content.Store
	access               *AccessControl
	trace                *TraceService
	now                  func() time.Time
	maxUpstreamRevisions int
}

func NewWorkbenchService(database *gorm.DB, contents content.Store, access *AccessControl) (*WorkbenchService, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("workbench database, content store and access control are required")
	}
	trace, _ := NewTraceService(database, access, contents)
	return &WorkbenchService{database: database, contents: contents, access: access, trace: trace, now: time.Now, maxUpstreamRevisions: 10_000}, nil
}

func (s *WorkbenchService) CreateBundle(ctx context.Context, projectID, actorID string, input CreateWorkbenchBundleInput) (WorkbenchBundle, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return WorkbenchBundle{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	if input.DeliverySliceID != nil {
		deliverySliceID := strings.TrimSpace(*input.DeliverySliceID)
		if deliverySliceID == "" || len(deliverySliceID) > 512 {
			return WorkbenchBundle{}, fmt.Errorf("%w: delivery slice id", ErrInvalidInput)
		}
		input.DeliverySliceID = &deliverySliceID
	}
	var workflowRunID *uuid.UUID
	var workflowContext *ApplicationBuildContext
	if input.workflowContext != nil {
		workflowContext, err = normalizeApplicationBuildContextForCreation(
			input.workflowContext,
			input.allowLegacyWorkflowContext,
		)
		if err != nil {
			return WorkbenchBundle{}, err
		}
		input.workflowContext = workflowContext
	}
	if input.WorkflowRunID != nil {
		parsed, err := uuid.Parse(*input.WorkflowRunID)
		if err != nil || input.RootOrdinal == nil || *input.RootOrdinal < 0 || input.ManifestGroupKey == nil ||
			strings.TrimSpace(*input.ManifestGroupKey) == "" || len(strings.TrimSpace(*input.ManifestGroupKey)) > 200 ||
			input.DeliverySliceID == nil {
			return WorkbenchBundle{}, fmt.Errorf("%w: workflow run, manifest group, root ordinal and delivery slice", ErrInvalidInput)
		}
		workflowRunID = &parsed
		var run storage.WorkflowRunModel
		if err := s.database.WithContext(ctx).
			Where("id = ?", parsed).Take(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return WorkbenchBundle{}, ErrConflict
			}
			return WorkbenchBundle{}, err
		}
		if run.ProjectID != projectUUID || run.StartedBy != actorUUID {
			return WorkbenchBundle{}, ErrConflict
		}
		if workflowContext != nil {
			if err := s.validateApplicationBuildContextForRun(ctx, run, input.DeliverySliceID, *workflowContext); err != nil {
				return WorkbenchBundle{}, err
			}
		}
		manifestGroupID, err := uuid.Parse(strings.TrimSpace(*input.ManifestGroupKey))
		if err != nil {
			return WorkbenchBundle{}, fmt.Errorf("%w: manifest group must be a workflow node run id", ErrInvalidInput)
		}
		var compilerNode storage.WorkflowNodeRunModel
		if err := s.database.WithContext(ctx).Select("id", "run_id", "node_type").
			Where("id = ? AND run_id = ?", manifestGroupID, parsed).Take(&compilerNode).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return WorkbenchBundle{}, ErrConflict
			}
			return WorkbenchBundle{}, err
		}
		if compilerNode.NodeType != string(domain.NodeManifestCompiler) {
			return WorkbenchBundle{}, ErrConflict
		}
		canonicalRunID := parsed.String()
		manifestGroupKey := manifestGroupID.String()
		input.WorkflowRunID = &canonicalRunID
		input.ManifestGroupKey = &manifestGroupKey
	} else if input.RootOrdinal != nil || input.ManifestGroupKey != nil {
		return WorkbenchBundle{}, fmt.Errorf("%w: manifest group and root ordinal require a workflow run", ErrInvalidInput)
	} else if workflowContext != nil {
		return WorkbenchBundle{}, fmt.Errorf("%w: workflow context requires a workflow run", ErrInvalidInput)
	}
	if workflowRunID != nil {
		existing, found, err := s.findExistingWorkflowRootBundle(
			ctx, projectUUID, *workflowRunID, *input.RootOrdinal, actorID, input,
		)
		if err != nil {
			return WorkbenchBundle{}, err
		}
		if found {
			return existing, nil
		}
		if workflowContext == nil {
			return WorkbenchBundle{}, fmt.Errorf("%w: new workflow bundles require trusted application build context", ErrInvalidInput)
		}
	}
	prototypeArtifactID, prototypeRevisionID, err := s.trace.validateRef(ctx, projectUUID, input.PrototypeRevision)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	var prototypeArtifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where("id = ? AND kind = 'prototype'", prototypeArtifactID).Take(&prototypeArtifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WorkbenchBundle{}, ErrNotFound
		}
		return WorkbenchBundle{}, err
	}
	var prototypeRevision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ?", prototypeRevisionID).Take(&prototypeRevision).Error; err != nil {
		return WorkbenchBundle{}, err
	}
	var health storage.ArtifactHealthModel
	if err := s.database.WithContext(ctx).Where("artifact_id = ?", prototypeArtifactID).Take(&health).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return WorkbenchBundle{}, err
	}
	formal := formalPrototypeWorkbenchInput(prototypeArtifact, prototypeRevision, health)
	waivers := []string{}
	if !formal {
		if !input.AllowStale || strings.TrimSpace(input.OverrideReason) == "" {
			return WorkbenchBundle{}, prototypeWorkbenchFormalGateError()
		}
		if _, err := s.access.Authorize(ctx, projectID, actorID, ActionAdmin); err != nil {
			return WorkbenchBundle{}, err
		}
		waivers = append(waivers, strings.TrimSpace(input.OverrideReason))
	}
	prototypeStored, err := s.contents.Get(ctx, prototypeRevision.ContentRef, prototypeRevision.ContentHash)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	var prototype map[string]any
	if err := json.Unmarshal(prototypeStored.Payload, &prototype); err != nil {
		return WorkbenchBundle{}, err
	}
	if exploratory, _ := prototype["exploratory"].(bool); exploratory && len(waivers) == 0 {
		return WorkbenchBundle{}, fmt.Errorf("%w: exploratory prototypes cannot be formal build input", ErrBlockingGate)
	}

	frozenSources, err := s.collectFrozenRevisionSources(ctx, projectUUID, prototypeRevisionID)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	pageRef, pageRefOK := versionRefFromValue(prototype["pageSpecRevision"])
	if !pageRefOK || !hasFrozenPrototypePageSpecSource(frozenSources, pageRef) {
		return WorkbenchBundle{}, fmt.Errorf("%w: Prototype pageSpecRevision is not its exact immutable required source", ErrBlockingGate)
	}
	upstream := frozenWorkbenchSourceRefs(frozenSources)
	classified, err := s.classifyAndValidateRefs(ctx, projectUUID, upstream)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	if len(classified.pageSpecs) != 1 || len(classified.blueprints) != 1 || len(classified.requirements) == 0 {
		return WorkbenchBundle{}, fmt.Errorf("%w: bundle needs one PageSpec, one Blueprint, and at least one approved requirement revision", ErrBlockingGate)
	}
	if len(waivers) == 0 {
		if err := s.validateFormalPrototypeWorkbenchSemantics(
			ctx, s.database, projectUUID, prototypeRevisionID, prototypeStored.Payload,
		); err != nil {
			return WorkbenchBundle{}, err
		}
	}
	workspaceRef, err := s.latestApprovedRefByKind(ctx, projectUUID, "workspace")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return WorkbenchBundle{}, err
	}

	bundleID := uuid.New()
	now := s.now().UTC()
	renderedFrames, generatedFrameContentIDs, err := s.renderedFrameAssets(ctx, projectID, bundleID, prototypeRevision, prototype)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	pendingContentIDs := append([]string(nil), generatedFrameContentIDs...)
	defer func() {
		for _, contentID := range pendingContentIDs {
			_ = s.contents.Abort(context.Background(), contentID)
		}
	}()
	bundle := WorkbenchBundle{
		ID: bundleID.String(), ProjectID: projectID, RootBuildManifestID: bundleID.String(),
		WorkflowRunID: input.WorkflowRunID, ManifestGroupKey: input.ManifestGroupKey,
		DeliverySliceID: input.DeliverySliceID, PageSpecRevision: classified.pageSpecs[0],
		PrototypeRevision: input.PrototypeRevision, RequirementRevisions: classified.requirements,
		BlueprintRevision: classified.blueprints[0], ContractRevisions: classified.contracts,
		DesignSystemRevisions: classified.designSystems, CurrentWorkspaceRevision: workspaceRef,
		ContextRevisions: classified.contexts, WorkflowContext: cloneApplicationBuildContext(workflowContext),
		RenderedFrames: renderedFrames, Assumptions: stringSlice(prototype["assumptions"]),
		Waivers: waivers, CreatedBy: actorID, CreatedAt: now,
	}
	bundle.SceneGraph = fragmentAsset(prototypeRevision, prototype, "scene", "layers", "scene-graph.json")
	bundle.InteractionManifest = fragmentAsset(prototypeRevision, prototype, "interactions", "interactions", "interactions.json")
	bundle.FixtureBundle = fragmentAsset(prototypeRevision, prototype, "fixtures", "fixtures", "fixtures.json")
	bundle.TokenManifest = fragmentAsset(prototypeRevision, prototype, "tokenBindings", "tokenBindings", "tokens.json")
	bundle.ComponentMapping = fragmentAsset(prototypeRevision, prototype, "componentBindings", "componentBindings", "components.json")
	bundle.TraceMatrix = fragmentAsset(prototypeRevision, prototype, "traceLinks", "traceLinks", "trace-matrix.json")
	acceptance := map[string]any{
		"requirements": classified.requirements,
		"pageSpec":     classified.pageSpecs[0],
		"traceLinks":   prototype["traceLinks"],
	}
	bundle.AcceptanceManifest = valueAsset(prototypeRevision, acceptance, "acceptance-manifest.json", "acceptance")
	manifestHash, err := workbenchBundleHash(bundle)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	bundle.ManifestHash = manifestHash
	payload, err := json.Marshal(bundle)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, projectID, "application_build_manifest", bundleID.String(), 1, payload)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	pendingContentIDs = append(pendingContentIDs, contentRef.ID)
	model := storage.ApplicationBuildManifestModel{
		ID: bundleID, ProjectID: projectUUID, RootManifestID: bundleID,
		WorkflowRunID:    workflowRunID,
		ManifestGroupKey: input.ManifestGroupKey,
		DeliverySliceID:  input.DeliverySliceID,
		SchemaVersion:    1, ContentStore: "mongo", ContentRef: contentRef.ID,
		ContentHash: contentRef.ContentHash, ManifestHash: bundle.ManifestHash,
		Status: "frozen", CreatedBy: actorUUID, CreatedAt: now,
	}
	if input.RootOrdinal != nil {
		rootOrdinal := *input.RootOrdinal
		model.RootOrdinal = &rootOrdinal
	}
	if workspaceRef != nil {
		workspaceRevisionID := uuid.MustParse(workspaceRef.RevisionID)
		model.WorkspaceRevisionID = &workspaceRevisionID
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		// The initial eligibility check gives callers a fast failure, while this
		// locked check closes the approval/health race before the immutable
		// manifest is committed. Explicit stale waivers intentionally bypass the
		// formal-input lock after their separate admin authorization above.
		if len(waivers) == 0 {
			locks, err := lockArtifactApprovalSourceClosure(
				ctx, transaction, projectUUID, prototypeArtifactID, prototypeRevisionID,
			)
			if err != nil {
				return err
			}
			artifact, artifactOK := locks.artifacts[prototypeArtifactID]
			revision, revisionOK := locks.revisions[prototypeRevisionID]
			health, healthOK := locks.health[prototypeArtifactID]
			if !artifactOK || !revisionOK || !healthOK || !formalPrototypeWorkbenchInput(artifact, revision, health) {
				return prototypeWorkbenchFormalGateError()
			}
			if err := s.validateFormalPrototypeWorkbenchSemantics(
				ctx, transaction, projectUUID, prototypeRevisionID, prototypeStored.Payload,
			); err != nil {
				return err
			}
		}
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if len(waivers) > 0 {
			if err := insertAudit(transaction, projectUUID, actorUUID, "workbench.stale_input_overridden", "application_build_manifest", bundleID.String(), map[string]any{"reason": waivers[0]}); err != nil {
				return err
			}
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "workbench.bundle_created", "application_build_manifest", bundleID.String(), map[string]any{"prototypeRevisionId": prototypeRevisionID.String()}); err != nil {
			return err
		}
		return enqueue(transaction, "application_build_manifest", bundleID.String(), "workbench.bundle_created", "worksflow.workbench.bundle.created", map[string]any{
			"projectId": projectID, "bundleId": bundleID.String(),
		})
	})
	if err != nil {
		// A retried manifest-compiler execution may race the first successful
		// insert. The database uniqueness constraint serializes that race; only
		// recover the already-frozen root after its complete immutable payload
		// and workflow identity match the request exactly.
		if workflowRunID != nil {
			existing, found, lookupErr := s.findExistingWorkflowRootBundle(
				ctx, projectUUID, *workflowRunID, *input.RootOrdinal, actorID, input,
			)
			if lookupErr != nil {
				return WorkbenchBundle{}, lookupErr
			}
			if found {
				return existing, nil
			}
		}
		return WorkbenchBundle{}, err
	}
	finalizeIDs := append([]string(nil), pendingContentIDs...)
	pendingContentIDs = nil
	var finalizeErrors []error
	for _, contentID := range finalizeIDs {
		if err := s.contents.Finalize(ctx, contentID); err != nil {
			finalizeErrors = append(finalizeErrors, err)
		}
	}
	if err := errors.Join(finalizeErrors...); err != nil {
		return WorkbenchBundle{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return bundle, nil
}

func (s *WorkbenchService) validateFormalPrototypeWorkbenchSemantics(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	prototypeRevisionID uuid.UUID,
	payload json.RawMessage,
) error {
	var sourceModels []storage.ArtifactRevisionSourceModel
	if err := database.WithContext(ctx).Where("revision_id = ?", prototypeRevisionID).
		Order("ordinal ASC").Find(&sourceModels).Error; err != nil {
		return err
	}
	artifacts := &ArtifactService{contents: s.contents}
	if err := artifacts.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", payload, sourceInputsFromRevisionModels(sourceModels),
	); err != nil {
		return err
	}
	if report := ValidateArtifactContent("prototype", payload); !report.Valid {
		encoded, _ := json.Marshal(report.Findings)
		return fmt.Errorf("%w: Prototype validation findings %s", ErrBlockingGate, encoded)
	}
	return nil
}

func formalPrototypeWorkbenchInput(
	artifact storage.ArtifactModel,
	revision storage.ArtifactRevisionModel,
	health storage.ArtifactHealthModel,
) bool {
	return artifact.ID != uuid.Nil &&
		artifact.Kind == "prototype" &&
		artifact.Lifecycle == "active" &&
		artifact.LatestApprovedRevisionID != nil &&
		revision.ID != uuid.Nil &&
		*artifact.LatestApprovedRevisionID == revision.ID &&
		revision.ArtifactID == artifact.ID &&
		revision.WorkflowStatus == "approved" &&
		health.ArtifactID == artifact.ID &&
		health.SyncStatus == "current"
}

func prototypeWorkbenchFormalGateError() error {
	return fmt.Errorf(
		"%w: prototype artifact must be active, the selected revision must be its exact latest approved revision, and dependency health must be current",
		ErrBlockingGate,
	)
}

func lockFormalPrototypeWorkbenchInput(
	transaction *gorm.DB,
	projectID uuid.UUID,
	artifactID uuid.UUID,
	revisionID uuid.UUID,
) error {
	var artifact storage.ArtifactModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND project_id = ? AND kind = 'prototype'", artifactID, projectID).
		Take(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return prototypeWorkbenchFormalGateError()
		}
		return err
	}
	var revision storage.ArtifactRevisionModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND artifact_id = ?", revisionID, artifactID).
		Take(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return prototypeWorkbenchFormalGateError()
		}
		return err
	}
	var health storage.ArtifactHealthModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("artifact_id = ?", artifactID).
		Take(&health).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return prototypeWorkbenchFormalGateError()
		}
		return err
	}
	if !formalPrototypeWorkbenchInput(artifact, revision, health) {
		return prototypeWorkbenchFormalGateError()
	}
	return nil
}

func (s *WorkbenchService) findExistingWorkflowRootBundle(
	ctx context.Context,
	projectID uuid.UUID,
	workflowRunID uuid.UUID,
	rootOrdinal int,
	actorID string,
	input CreateWorkbenchBundleInput,
) (WorkbenchBundle, bool, error) {
	var models []storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).Where(
		"project_id = ? AND workflow_run_id = ? AND manifest_group_key = ? AND root_ordinal = ? AND derived_from_id IS NULL",
		projectID, workflowRunID, *input.ManifestGroupKey, rootOrdinal,
	).Order("id ASC").Limit(2).Find(&models).Error; err != nil {
		return WorkbenchBundle{}, false, err
	}
	if len(models) == 0 {
		return WorkbenchBundle{}, false, nil
	}
	if len(models) != 1 {
		return WorkbenchBundle{}, false, ErrConflict
	}
	model := models[0]
	if model.RootManifestID != model.ID || model.DerivedFromID != nil || model.WorkflowRunID == nil ||
		*model.WorkflowRunID != workflowRunID || model.RootOrdinal == nil || *model.RootOrdinal != rootOrdinal ||
		!optionalStringsEqual(model.ManifestGroupKey, input.ManifestGroupKey) ||
		!optionalStringsEqual(model.DeliverySliceID, input.DeliverySliceID) {
		return WorkbenchBundle{}, false, ErrConflict
	}
	bundle, err := s.loadBundleContent(ctx, model)
	if err != nil {
		return WorkbenchBundle{}, false, err
	}
	if bundle.RootBuildManifestID != model.ID.String() || bundle.CreatedBy != actorID ||
		!sameWorkflowRunID(bundle.WorkflowRunID, workflowRunID) ||
		!optionalStringsEqual(bundle.ManifestGroupKey, input.ManifestGroupKey) ||
		!optionalStringsEqual(bundle.DeliverySliceID, input.DeliverySliceID) ||
		!applicationBuildContextsEqual(bundle.WorkflowContext, input.workflowContext) ||
		!exactWorkbenchVersionRef(bundle.PrototypeRevision, input.PrototypeRevision) {
		return WorkbenchBundle{}, false, ErrConflict
	}
	if len(bundle.Waivers) > 0 && (!input.AllowStale || len(bundle.Waivers) != 1 ||
		bundle.Waivers[0] != strings.TrimSpace(input.OverrideReason)) {
		return WorkbenchBundle{}, false, ErrConflict
	}
	return bundle, true, nil
}

func sameWorkflowRunID(value *string, expected uuid.UUID) bool {
	if value == nil {
		return false
	}
	parsed, err := uuid.Parse(strings.TrimSpace(*value))
	return err == nil && parsed == expected
}

func optionalStringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func exactWorkbenchVersionRef(left, right VersionRef) bool {
	return left.ArtifactID == right.ArtifactID && left.RevisionID == right.RevisionID &&
		left.ContentHash == right.ContentHash && stringPointerEqual(left.AnchorID, right.AnchorID)
}

func normalizeApplicationBuildContext(value *ApplicationBuildContext) (*ApplicationBuildContext, error) {
	return normalizeApplicationBuildContextForCreation(value, false)
}

func normalizeApplicationBuildContextForCreation(
	value *ApplicationBuildContext,
	allowLegacy bool,
) (*ApplicationBuildContext, error) {
	if value == nil {
		return nil, nil
	}
	result := *value
	legacy := allowLegacy && legacyApplicationBuildContext(result)
	if legacy {
		if strings.TrimSpace(result.Definition.ID) == "" || result.Definition.Version < 1 || !domain.IsCanonicalHash(result.Definition.Hash) {
			return nil, fmt.Errorf("%w: historical workflow definition context", ErrInvalidInput)
		}
	} else {
		if err := result.Definition.Validate(); err != nil {
			return nil, fmt.Errorf("%w: workflow definition context", ErrInvalidInput)
		}
		if err := result.ExecutionProfile.Validate(); err != nil || result.Definition.ExecutionProfile != result.ExecutionProfile {
			return nil, fmt.Errorf("%w: workflow execution profile context", ErrInvalidInput)
		}
	}
	if err := result.InputManifest.Validate(); err != nil {
		return nil, fmt.Errorf("%w: workflow input manifest context", ErrInvalidInput)
	}
	result.DeliverySliceID = strings.TrimSpace(result.DeliverySliceID)
	if result.DeliverySliceID == "" {
		return nil, fmt.Errorf("%w: workflow delivery-slice context", ErrInvalidInput)
	}
	canonicalManifest, err := domain.CanonicalJSON(result.InputManifest)
	if err != nil {
		return nil, fmt.Errorf("%w: workflow input manifest context", ErrInvalidInput)
	}
	if err := json.Unmarshal(canonicalManifest, &result.InputManifest); err != nil {
		return nil, fmt.Errorf("%w: workflow input manifest context", ErrInvalidInput)
	}
	runScope := result.RunScope
	if len(runScope) == 0 {
		runScope = json.RawMessage(`{}`)
	}
	canonicalRunScope, err := domain.CanonicalJSON(runScope)
	if err != nil {
		return nil, fmt.Errorf("%w: workflow run scope", ErrInvalidInput)
	}
	result.RunScope = canonicalRunScope
	if result.OutputContract == nil || result.OutputContract.Validate() != nil {
		return nil, fmt.Errorf("%w: workflow output contract context", ErrInvalidInput)
	}
	outputContract := *result.OutputContract
	outputContract.ProducedArtifactKinds = append([]string(nil), result.OutputContract.ProducedArtifactKinds...)
	result.OutputContract = &outputContract
	return &result, nil
}

func cloneApplicationBuildContext(value *ApplicationBuildContext) *ApplicationBuildContext {
	if value == nil {
		return nil
	}
	copy := *value
	copy.RunScope = append(json.RawMessage(nil), value.RunScope...)
	if canonicalManifest, err := domain.CanonicalJSON(value.InputManifest); err == nil {
		_ = json.Unmarshal(canonicalManifest, &copy.InputManifest)
	}
	if value.OutputContract != nil {
		outputContract := *value.OutputContract
		outputContract.ProducedArtifactKinds = append([]string(nil), value.OutputContract.ProducedArtifactKinds...)
		copy.OutputContract = &outputContract
	}
	return &copy
}

func applicationBuildContextsEqual(left, right *ApplicationBuildContext) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftCanonical, leftErr := domain.CanonicalJSON(left)
	rightCanonical, rightErr := domain.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func (s *WorkbenchService) validateApplicationBuildContextForRun(
	ctx context.Context,
	run storage.WorkflowRunModel,
	deliverySliceID *string,
	buildContext ApplicationBuildContext,
) error {
	if deliverySliceID == nil || strings.TrimSpace(*deliverySliceID) != buildContext.DeliverySliceID {
		return fmt.Errorf("%w: workflow delivery slice context drifted", ErrConflict)
	}
	var version storage.WorkflowDefinitionVersionModel
	if err := s.database.WithContext(ctx).Where("id = ?", run.DefinitionVersionID).Take(&version).Error; err != nil {
		return err
	}
	rowProfile := domain.WorkflowExecutionProfileRef{Version: version.ExecutionProfileVersion, Hash: version.ExecutionProfileHash}
	runProfile := domain.WorkflowExecutionProfileRef{Version: run.ExecutionProfileVersion, Hash: run.ExecutionProfileHash}
	if legacyApplicationBuildContext(buildContext) {
		legacy := domain.WorkflowExecutionProfileRef{Version: legacyWorkflowExecutionProfileVersion, Hash: legacyWorkflowExecutionProfileHash}
		if rowProfile != legacy || runProfile != legacy {
			return fmt.Errorf("%w: historical workflow context is not backed by a legacy execution profile", ErrConflict)
		}
	} else if buildContext.ExecutionProfile != rowProfile || buildContext.ExecutionProfile != runProfile || buildContext.Definition.ExecutionProfile != rowProfile {
		return fmt.Errorf("%w: workflow execution profile context drifted", ErrConflict)
	}
	var definition domain.WorkflowDefinition
	if err := json.Unmarshal(version.Content, &definition); err != nil {
		return ErrConflict
	}
	if version.ContentHash != buildContext.Definition.Hash || definition.Ref() != buildContext.Definition || definition.OutputContract == nil ||
		!applicationBuildOutputContractsEqual(definition.OutputContract, buildContext.OutputContract) {
		return fmt.Errorf("%w: workflow definition context drifted", ErrConflict)
	}
	if run.InputManifestID == nil || run.InputManifestID.String() != buildContext.InputManifest.ID {
		return fmt.Errorf("%w: workflow input manifest context drifted", ErrConflict)
	}
	var manifestModel storage.InputManifestModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND project_id = ?", *run.InputManifestID, run.ProjectID,
	).Take(&manifestModel).Error; err != nil {
		return err
	}
	if manifestModel.ManifestHash != buildContext.InputManifest.Hash || manifestModel.Kind != buildContext.InputManifest.JobType {
		return fmt.Errorf("%w: workflow input manifest identity drifted", ErrConflict)
	}
	stored, err := s.contents.Get(ctx, manifestModel.ContentRef, manifestModel.ContentHash)
	if err != nil {
		return err
	}
	var manifest domain.InputManifest
	if err := json.Unmarshal(stored.Payload, &manifest); err != nil || manifest.Validate() != nil {
		return ErrConflict
	}
	if !applicationBuildInputManifestsEqual(manifest, buildContext.InputManifest) {
		return fmt.Errorf("%w: workflow input manifest semantics drifted", ErrConflict)
	}
	if !canonicalJSONEqual(run.Scope, buildContext.RunScope) {
		return fmt.Errorf("%w: workflow run scope drifted", ErrConflict)
	}
	return nil
}

func applicationBuildOutputContractsEqual(left, right *domain.WorkflowOutputContract) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftCanonical, leftErr := domain.CanonicalJSON(left)
	rightCanonical, rightErr := domain.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func applicationBuildInputManifestsEqual(left, right domain.InputManifest) bool {
	leftCanonical, leftErr := domain.CanonicalJSON(left)
	rightCanonical, rightErr := domain.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func canonicalJSONEqual(left, right json.RawMessage) bool {
	leftCanonical, leftErr := domain.CanonicalJSON(left)
	rightCanonical, rightErr := domain.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func (s *WorkbenchService) GetBundle(ctx context.Context, bundleID, actorID string) (WorkbenchBundle, error) {
	id, err := uuid.Parse(bundleID)
	if err != nil {
		return WorkbenchBundle{}, fmt.Errorf("%w: bundle id", ErrInvalidInput)
	}
	var model storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WorkbenchBundle{}, ErrNotFound
		}
		return WorkbenchBundle{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionView); err != nil {
		return WorkbenchBundle{}, err
	}
	if err := ensureWorkflowManifestGroupActivated(ctx, s.database, model); err != nil {
		return WorkbenchBundle{}, err
	}
	return s.loadBundleContent(ctx, model)
}

func (s *WorkbenchService) GetBundleForGeneration(ctx context.Context, bundleID, actorID string) (WorkbenchBundle, error) {
	id, err := uuid.Parse(strings.TrimSpace(bundleID))
	if err != nil {
		return WorkbenchBundle{}, fmt.Errorf("%w: bundle id", ErrInvalidInput)
	}
	var model storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WorkbenchBundle{}, ErrNotFound
		}
		return WorkbenchBundle{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionEdit); err != nil {
		return WorkbenchBundle{}, err
	}
	if err := ensureWorkflowManifestGroupActivated(ctx, s.database, model); err != nil {
		return WorkbenchBundle{}, err
	}
	if model.Status != "frozen" {
		return WorkbenchBundle{}, fmt.Errorf("%w: build manifest is not frozen", ErrBlockingGate)
	}
	var childCount int64
	if err := s.database.WithContext(ctx).Model(&storage.ApplicationBuildManifestModel{}).
		Where("derived_from_id = ?", model.ID).Count(&childCount).Error; err != nil {
		return WorkbenchBundle{}, err
	}
	if childCount != 0 {
		return WorkbenchBundle{}, fmt.Errorf("%w: build manifest is not the active lineage leaf", ErrBlockingGate)
	}
	rootID := model.RootManifestID
	if rootID == uuid.Nil {
		rootID = model.ID
	}
	var applied int64
	if err := s.database.WithContext(ctx).Table("implementation_proposals AS proposals").
		Joins("JOIN application_build_manifests AS manifests ON manifests.id = proposals.build_manifest_id").
		Where(
			"proposals.project_id = ? AND manifests.root_manifest_id = ? AND proposals.status IN ?",
			model.ProjectID, rootID, []string{"applied", "partially_applied"},
		).Count(&applied).Error; err != nil {
		return WorkbenchBundle{}, err
	}
	if applied != 0 {
		return WorkbenchBundle{}, fmt.Errorf("%w: build manifest root is already applied", ErrBlockingGate)
	}
	if err := ensureWorkflowManifestOrdinalReady(ctx, s.database, model); err != nil {
		return WorkbenchBundle{}, err
	}
	bundle, err := s.loadBundleContent(ctx, model)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	currentWorkspace, err := s.currentApprovedWorkspaceRef(ctx, model.ProjectID)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	if !optionalVersionRefsEqual(bundle.CurrentWorkspaceRevision, currentWorkspace) {
		return WorkbenchBundle{}, fmt.Errorf("%w: build manifest must be rebased to the current workspace revision", ErrProposalStale)
	}
	return bundle, nil
}

func ensureWorkflowManifestOrdinalReady(
	ctx context.Context,
	database *gorm.DB,
	manifest storage.ApplicationBuildManifestModel,
) error {
	if manifest.WorkflowRunID == nil {
		return nil
	}
	if manifest.RootOrdinal == nil || *manifest.RootOrdinal < 0 {
		return fmt.Errorf("%w: workflow build manifest has no root ordinal", ErrBlockingGate)
	}
	if manifest.ManifestGroupKey == nil || strings.TrimSpace(*manifest.ManifestGroupKey) == "" {
		return fmt.Errorf("%w: workflow build manifest has no manifest group", ErrBlockingGate)
	}
	rootID := manifest.RootManifestID
	if rootID == uuid.Nil {
		rootID = manifest.ID
	}
	var roots []storage.ApplicationBuildManifestModel
	if err := database.WithContext(ctx).Where(
		"project_id = ? AND workflow_run_id = ? AND manifest_group_key = ? AND derived_from_id IS NULL",
		manifest.ProjectID, *manifest.WorkflowRunID, *manifest.ManifestGroupKey,
	).Order("root_ordinal ASC NULLS LAST, id ASC").Find(&roots).Error; err != nil {
		return err
	}
	if len(roots) == 0 || *manifest.RootOrdinal >= len(roots) {
		return ErrConflict
	}
	for ordinal, root := range roots {
		if root.RootOrdinal == nil || *root.RootOrdinal != ordinal {
			return ErrConflict
		}
		if !optionalStringsEqual(root.ManifestGroupKey, manifest.ManifestGroupKey) {
			return ErrConflict
		}
		if ordinal == *manifest.RootOrdinal && root.ID != rootID {
			return ErrConflict
		}
		var applied int64
		if err := database.WithContext(ctx).Table("implementation_proposals AS proposals").
			Joins("JOIN application_build_manifests AS manifests ON manifests.id = proposals.build_manifest_id").
			Where(
				"proposals.project_id = ? AND manifests.root_manifest_id = ? AND manifests.workflow_run_id = ? AND proposals.status IN ? AND proposals.applied_at IS NOT NULL",
				manifest.ProjectID, root.ID, *manifest.WorkflowRunID, []string{"applied", "partially_applied"},
			).Count(&applied).Error; err != nil {
			return err
		}
		expected := int64(0)
		if ordinal < *manifest.RootOrdinal {
			expected = 1
		}
		if applied != expected {
			return fmt.Errorf("%w: workflow build manifests must apply in frozen root order", ErrBlockingGate)
		}
	}
	return nil
}

func (s *WorkbenchService) GetLineageState(ctx context.Context, rootID, actorID string) (WorkbenchLineageState, error) {
	requestedID, err := uuid.Parse(strings.TrimSpace(rootID))
	if err != nil {
		return WorkbenchLineageState{}, fmt.Errorf("%w: build manifest lineage id", ErrInvalidInput)
	}
	var requested storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).Where("id = ?", requestedID).Take(&requested).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WorkbenchLineageState{}, ErrNotFound
		}
		return WorkbenchLineageState{}, err
	}
	if _, err := s.access.Authorize(ctx, requested.ProjectID.String(), actorID, ActionView); err != nil {
		return WorkbenchLineageState{}, err
	}
	if err := ensureWorkflowManifestGroupActivated(ctx, s.database, requested); err != nil {
		return WorkbenchLineageState{}, err
	}
	rootManifestID := requested.RootManifestID
	if rootManifestID == uuid.Nil {
		rootManifestID = requested.ID
	}
	if err := validateBuildManifestModelLineage(
		ctx, s.database, requested, rootManifestID, requested.ProjectID,
	); err != nil {
		return WorkbenchLineageState{}, err
	}
	var root storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).
		Where("id = ? AND project_id = ?", rootManifestID, requested.ProjectID).
		Take(&root).Error; err != nil {
		return WorkbenchLineageState{}, err
	}
	if root.DerivedFromID != nil || (root.RootManifestID != uuid.Nil && root.RootManifestID != root.ID) {
		return WorkbenchLineageState{}, ErrConflict
	}
	var models []storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).
		Where("project_id = ? AND root_manifest_id = ?", root.ProjectID, rootManifestID).
		Order("created_at ASC, id ASC").Find(&models).Error; err != nil {
		return WorkbenchLineageState{}, err
	}
	if len(models) == 0 {
		return WorkbenchLineageState{}, ErrConflict
	}
	models, err = orderedBuildManifestLineage(models, rootManifestID)
	if err != nil {
		return WorkbenchLineageState{}, err
	}
	var appliedProposalCount int64
	if err := s.database.WithContext(ctx).Table("implementation_proposals AS proposals").
		Joins("JOIN application_build_manifests AS manifests ON manifests.id = proposals.build_manifest_id").
		Where(
			"proposals.project_id = ? AND manifests.root_manifest_id = ? AND proposals.status IN ?",
			root.ProjectID, rootManifestID, []string{"applied", "partially_applied"},
		).Count(&appliedProposalCount).Error; err != nil {
		return WorkbenchLineageState{}, err
	}
	if appliedProposalCount > 1 {
		return WorkbenchLineageState{}, ErrConflict
	}
	currentWorkspace, err := s.currentApprovedWorkspaceRef(ctx, root.ProjectID)
	if err != nil {
		return WorkbenchLineageState{}, err
	}
	state := WorkbenchLineageState{
		RootBundleID: rootManifestID.String(), CurrentWorkspaceRevision: currentWorkspace,
		Lineage: make([]WorkbenchLineageEntry, 0, len(models)),
	}
	bundles := make(map[uuid.UUID]WorkbenchBundle, len(models))
	var appliedProposal *ImplementationProposal
	var appliedManifestID uuid.UUID
	for _, model := range models {
		if !optionalUUIDsEqual(model.WorkflowRunID, root.WorkflowRunID) ||
			!optionalStringsEqual(model.DeliverySliceID, root.DeliverySliceID) {
			return WorkbenchLineageState{}, ErrConflict
		}
		if root.WorkflowRunID == nil {
			if model.ManifestGroupKey != nil || root.ManifestGroupKey != nil || model.RootOrdinal != nil || root.RootOrdinal != nil {
				return WorkbenchLineageState{}, ErrConflict
			}
		} else if !optionalStringsEqual(model.ManifestGroupKey, root.ManifestGroupKey) || model.RootOrdinal == nil ||
			root.RootOrdinal == nil || *model.RootOrdinal != *root.RootOrdinal {
			return WorkbenchLineageState{}, ErrConflict
		}
		if err := validateBuildManifestModelLineage(ctx, s.database, model, rootManifestID, root.ProjectID); err != nil {
			return WorkbenchLineageState{}, err
		}
		bundle, err := s.loadBundleContent(ctx, model)
		if err != nil {
			return WorkbenchLineageState{}, err
		}
		bundles[model.ID] = bundle
		proposal, err := s.latestNonStaleLineageProposal(ctx, model)
		if err != nil {
			return WorkbenchLineageState{}, err
		}
		entry := WorkbenchLineageEntry{
			BundleID: model.ID.String(), DerivedFromBuildManifestID: uuidStringPointer(model.DerivedFromID),
			WorkspaceRevision: cloneVersionRef(bundle.CurrentWorkspaceRevision),
			Status:            model.Status, CreatedAt: model.CreatedAt,
		}
		if proposal != nil {
			entry.LatestProposal = &WorkbenchLineageProposalSummary{
				ID: proposal.ID, Status: proposal.Status, Version: proposal.Version, CreatedAt: proposal.CreatedAt,
			}
			if proposal.Status == "applied" || proposal.Status == "partially_applied" {
				if appliedProposal != nil && appliedProposal.ID != proposal.ID {
					return WorkbenchLineageState{}, ErrConflict
				}
				proposalCopy := *proposal
				appliedProposal = &proposalCopy
				appliedManifestID = model.ID
			}
		}
		state.Lineage = append(state.Lineage, entry)
	}
	if appliedProposal != nil {
		if appliedManifestID != models[len(models)-1].ID {
			return WorkbenchLineageState{}, ErrConflict
		}
		state.ActiveBundle = bundles[appliedManifestID]
		state.CurrentProposal = appliedProposal
		return state, nil
	}
	latestModel := models[len(models)-1]
	state.ActiveBundle = bundles[latestModel.ID]
	currentProposal, err := s.latestNonStaleLineageProposal(ctx, latestModel)
	if err != nil {
		return WorkbenchLineageState{}, err
	}
	state.CurrentProposal = currentProposal
	return state, nil
}

func orderedBuildManifestLineage(
	models []storage.ApplicationBuildManifestModel,
	rootID uuid.UUID,
) ([]storage.ApplicationBuildManifestModel, error) {
	byID := make(map[uuid.UUID]storage.ApplicationBuildManifestModel, len(models))
	children := make(map[uuid.UUID]uuid.UUID, len(models))
	for _, model := range models {
		if model.ID == uuid.Nil {
			return nil, ErrConflict
		}
		if _, duplicate := byID[model.ID]; duplicate {
			return nil, ErrConflict
		}
		byID[model.ID] = model
	}
	root, exists := byID[rootID]
	if !exists || root.DerivedFromID != nil {
		return nil, ErrConflict
	}
	for _, model := range models {
		if model.ID == rootID {
			continue
		}
		if model.DerivedFromID == nil {
			return nil, ErrConflict
		}
		if _, exists := byID[*model.DerivedFromID]; !exists {
			return nil, ErrConflict
		}
		if _, duplicateChild := children[*model.DerivedFromID]; duplicateChild {
			return nil, ErrConflict
		}
		children[*model.DerivedFromID] = model.ID
	}
	ordered := make([]storage.ApplicationBuildManifestModel, 0, len(models))
	visited := make(map[uuid.UUID]bool, len(models))
	currentID := rootID
	for {
		if visited[currentID] {
			return nil, ErrConflict
		}
		visited[currentID] = true
		ordered = append(ordered, byID[currentID])
		nextID, exists := children[currentID]
		if !exists {
			break
		}
		currentID = nextID
	}
	if len(ordered) != len(models) {
		return nil, ErrConflict
	}
	return ordered, nil
}

func optionalUUIDsEqual(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *WorkbenchService) currentApprovedWorkspaceRef(ctx context.Context, projectID uuid.UUID) (*VersionRef, error) {
	var artifacts []storage.ArtifactModel
	if err := s.database.WithContext(ctx).
		Where("project_id = ? AND kind = 'workspace' AND lifecycle = 'active' AND latest_approved_revision_id IS NOT NULL", projectID).
		Order("artifact_key ASC").Limit(2).Find(&artifacts).Error; err != nil {
		return nil, err
	}
	if len(artifacts) == 0 {
		return nil, nil
	}
	if len(artifacts) != 1 {
		return nil, ErrConflict
	}
	artifact := artifacts[0]
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND artifact_id = ? AND workflow_status = 'approved'", *artifact.LatestApprovedRevisionID, artifact.ID,
	).Take(&revision).Error; err != nil {
		return nil, err
	}
	return &VersionRef{
		ArtifactID: artifact.ID.String(), RevisionID: revision.ID.String(), ContentHash: revision.ContentHash,
	}, nil
}

func (s *WorkbenchService) latestNonStaleLineageProposal(
	ctx context.Context,
	manifest storage.ApplicationBuildManifestModel,
) (*ImplementationProposal, error) {
	var model storage.ImplementationProposalModel
	err := s.database.WithContext(ctx).
		Where("project_id = ? AND build_manifest_id = ? AND status <> 'stale'", manifest.ProjectID, manifest.ID).
		Order("CASE WHEN status IN ('applied', 'partially_applied') THEN 0 ELSE 1 END ASC").
		Order("applied_at DESC NULLS LAST, created_at DESC, id DESC").
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
	if err != nil {
		return nil, err
	}
	var proposal ImplementationProposal
	if err := json.Unmarshal(stored.Payload, &proposal); err != nil {
		return nil, err
	}
	normalizeLegacyImplementationExecution(&proposal, model)
	payloadHash, err := implementationPayloadHash(proposal)
	if err != nil || payloadHash != proposal.PayloadHash || payloadHash != model.PayloadHash ||
		proposal.ID != model.ID.String() || proposal.ProjectID != model.ProjectID.String() ||
		proposal.BuildManifestID != model.BuildManifestID.String() || proposal.Status != model.Status || proposal.Version != model.Version ||
		string(proposal.ExecutionSource) != model.ExecutionSource || !optionalStringMatchesUUID(proposal.ConversationCommandID, model.ConversationCommandID) ||
		!optionalStringMatchesUUID(proposal.SupersedesProposalID, model.SupersedesProposalID) ||
		proposal.InstructionHash != stringValue(model.InstructionHash) || proposal.AIProvider != stringValue(model.AIProvider) || proposal.AIModel != stringValue(model.AIModel) {
		return nil, ErrConflict
	}
	return &proposal, nil
}

func validateBuildManifestModelLineage(
	ctx context.Context,
	database *gorm.DB,
	manifest storage.ApplicationBuildManifestModel,
	rootID uuid.UUID,
	projectID uuid.UUID,
) error {
	visited := map[uuid.UUID]bool{}
	current := manifest
	expectedDeliverySliceID := manifest.DeliverySliceID
	for {
		if current.ID == uuid.Nil || current.ProjectID != projectID || visited[current.ID] {
			return ErrConflict
		}
		if !optionalStringsEqual(current.DeliverySliceID, expectedDeliverySliceID) {
			return ErrConflict
		}
		visited[current.ID] = true
		currentRootID := current.RootManifestID
		if currentRootID == uuid.Nil {
			currentRootID = current.ID
		}
		if currentRootID != rootID {
			return ErrConflict
		}
		if current.ID == rootID {
			if current.DerivedFromID != nil {
				return ErrConflict
			}
			return nil
		}
		if current.DerivedFromID == nil || len(visited) > 10_000 {
			return ErrConflict
		}
		var parent storage.ApplicationBuildManifestModel
		if err := database.WithContext(ctx).
			Where("id = ? AND project_id = ? AND root_manifest_id = ?", *current.DerivedFromID, projectID, rootID).
			Take(&parent).Error; err != nil {
			return err
		}
		current = parent
	}
}

func (s *WorkbenchService) Rebase(ctx context.Context, bundleID, actorID string, input RebaseWorkbenchBundleInput) (WorkbenchBundle, error) {
	parentID, err := uuid.Parse(strings.TrimSpace(bundleID))
	if err != nil {
		return WorkbenchBundle{}, fmt.Errorf("%w: bundle id", ErrInvalidInput)
	}
	if input.WorkspaceRevision.AnchorID != nil && strings.TrimSpace(*input.WorkspaceRevision.AnchorID) != "" {
		return WorkbenchBundle{}, fmt.Errorf("%w: workspace revision anchor", ErrInvalidInput)
	}
	var authorizationModel storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).Where("id = ?", parentID).Take(&authorizationModel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WorkbenchBundle{}, ErrNotFound
		}
		return WorkbenchBundle{}, err
	}
	if _, err := s.access.Authorize(ctx, authorizationModel.ProjectID.String(), actorID, ActionEdit); err != nil {
		return WorkbenchBundle{}, err
	}
	if err := ensureWorkflowManifestGroupActivated(ctx, s.database, authorizationModel); err != nil {
		return WorkbenchBundle{}, err
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return WorkbenchBundle{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}

	var result WorkbenchBundle
	var pendingContentIDs []string
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		// Workspace revisions and implementation apply both lock the workspace
		// artifact first. Keep the same lock order here before locking manifest
		// lineage rows so apply/rebase races cannot deadlock each other.
		workspaceArtifact, workspaceRevision, err := validateCurrentWorkspaceRevision(
			transaction, authorizationModel.ProjectID, input.WorkspaceRevision,
		)
		if err != nil {
			return err
		}
		rootID := authorizationModel.RootManifestID
		if rootID == uuid.Nil {
			rootID = authorizationModel.ID
		}
		var root storage.ApplicationBuildManifestModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ?", rootID, authorizationModel.ProjectID).Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return err
		}
		if root.RootManifestID != uuid.Nil && root.RootManifestID != root.ID {
			return ErrConflict
		}
		now := s.now().UTC()
		var liveGenerationClaims int64
		if err := transaction.Model(&storage.ImplementationGenerationClaimModel{}).Where(
			"project_id = ? AND root_manifest_id = ? AND status = 'processing' AND claim_expires_at >= ?",
			authorizationModel.ProjectID, rootID, now,
		).Count(&liveGenerationClaims).Error; err != nil {
			return err
		}
		if liveGenerationClaims != 0 {
			return fmt.Errorf("%w: Workbench generation is active for this manifest root", ErrConflict)
		}
		var pendingConversationProposals int64
		if err := transaction.Table("implementation_proposals AS proposals").
			Joins("JOIN application_build_manifests AS manifests ON manifests.id = proposals.build_manifest_id").
			Joins("JOIN conversation_commands AS commands ON commands.id = proposals.conversation_command_id").
			Where(
				"proposals.project_id = ? AND manifests.root_manifest_id = ? AND proposals.execution_source = ? AND proposals.status IN ? AND commands.status = ?",
				authorizationModel.ProjectID, rootID, ImplementationSourceConversationCommand,
				[]string{"open", "reviewing", "ready"}, "pending",
			).Count(&pendingConversationProposals).Error; err != nil {
			return err
		}
		if pendingConversationProposals != 0 {
			return fmt.Errorf("%w: a conversation command is finalizing this manifest root", ErrConflict)
		}
		var parent storage.ApplicationBuildManifestModel
		if rootID == parentID {
			parent = root
		} else if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", parentID).Take(&parent).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if parent.ProjectID != authorizationModel.ProjectID || parent.RootManifestID != rootID {
			return ErrConflict
		}
		var appliedProposalCount int64
		if err := transaction.Table("implementation_proposals AS proposals").
			Joins("JOIN application_build_manifests AS manifests ON manifests.id = proposals.build_manifest_id").
			Where(
				"proposals.project_id = ? AND manifests.root_manifest_id = ? AND proposals.status IN ?",
				parent.ProjectID, rootID, []string{"applied", "partially_applied"},
			).Count(&appliedProposalCount).Error; err != nil {
			return err
		}
		if appliedProposalCount != 0 {
			return fmt.Errorf("%w: build manifest root is already applied", ErrBlockingGate)
		}
		var directChildren []storage.ApplicationBuildManifestModel
		if err := transaction.Where("derived_from_id = ?", parent.ID).
			Order("created_at ASC, id ASC").Limit(2).Find(&directChildren).Error; err != nil {
			return err
		}
		if len(directChildren) > 1 {
			return ErrConflict
		}
		if len(directChildren) == 1 {
			child := directChildren[0]
			if child.ProjectID != parent.ProjectID || child.RootManifestID != rootID ||
				child.WorkspaceRevisionID == nil || *child.WorkspaceRevisionID != workspaceRevision.ID {
				return ErrConflict
			}
			result, err = s.loadBundleContent(ctx, child)
			return err
		}
		if parent.Status != "frozen" {
			return ErrConflict
		}
		if err := ensureWorkflowManifestOrdinalReady(ctx, transaction, parent); err != nil {
			return err
		}

		parentBundle, err := s.loadBundleContent(ctx, parent)
		if err != nil {
			return err
		}

		if parentBundle.CurrentWorkspaceRevision != nil {
			parentArtifactID, parseErr := uuid.Parse(parentBundle.CurrentWorkspaceRevision.ArtifactID)
			if parseErr != nil || parentArtifactID != workspaceArtifact.ID {
				return ErrProposalStale
			}
			parentRevisionID, parseErr := uuid.Parse(parentBundle.CurrentWorkspaceRevision.RevisionID)
			if parseErr != nil || (parent.WorkspaceRevisionID != nil && *parent.WorkspaceRevisionID != parentRevisionID) {
				return ErrConflict
			}
			descends, lineageErr := workspaceRevisionDescendsFrom(
				transaction, workspaceArtifact.ID, workspaceRevision.ID, parentRevisionID,
			)
			if lineageErr != nil {
				return lineageErr
			}
			if !descends || workspaceRevision.ID == parentRevisionID {
				return ErrProposalStale
			}
		} else if parent.WorkspaceRevisionID != nil {
			return ErrConflict
		}

		now = s.now().UTC()
		rebasedID := uuid.New()
		result, err = deriveWorkbenchBundle(
			parentBundle, rebasedID, rootID, parent.ID, input.WorkspaceRevision, actorID, now,
		)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		contentRef, err := s.contents.PutPending(
			ctx, parent.ProjectID.String(), "application_build_manifest", rebasedID.String(), 1, payload,
		)
		if err != nil {
			return err
		}
		pendingContentIDs = append(pendingContentIDs, contentRef.ID)
		derivedFromID := parent.ID
		model := storage.ApplicationBuildManifestModel{
			ID: rebasedID, ProjectID: parent.ProjectID, WorkflowRunID: parent.WorkflowRunID,
			ManifestGroupKey: parent.ManifestGroupKey,
			DeliverySliceID:  parent.DeliverySliceID,
			RootManifestID:   rootID, DerivedFromID: &derivedFromID, WorkspaceRevisionID: &workspaceRevision.ID,
			RootOrdinal:   parent.RootOrdinal,
			SchemaVersion: parent.SchemaVersion, ContentStore: parent.ContentStore, ContentRef: contentRef.ID,
			ContentHash: contentRef.ContentHash, ManifestHash: result.ManifestHash, Status: "frozen",
			CreatedBy: actorUUID, CreatedAt: now,
		}
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		staleContentIDs, err := s.stageManifestProposalsStale(ctx, transaction, parent, actorUUID, now)
		pendingContentIDs = append(pendingContentIDs, staleContentIDs...)
		if err != nil {
			return err
		}
		invalidationReason := "rebased_to:" + rebasedID.String()
		invalidated := transaction.Model(&storage.ApplicationBuildManifestModel{}).
			Where("id = ? AND status = 'frozen'", parent.ID).
			Updates(map[string]any{
				"status": "invalidated", "invalidated_at": now, "invalidation_reason": invalidationReason,
			})
		if invalidated.Error != nil {
			return invalidated.Error
		}
		if invalidated.RowsAffected != 1 {
			return ErrConflict
		}
		metadata := map[string]any{
			"parentBuildManifestId": parent.ID.String(), "rootBuildManifestId": rootID.String(),
			"workspaceArtifactId": workspaceArtifact.ID.String(), "workspaceRevisionId": workspaceRevision.ID.String(),
		}
		if err := insertAudit(
			transaction, parent.ProjectID, actorUUID, "workbench.bundle_rebased",
			"application_build_manifest", rebasedID.String(), metadata,
		); err != nil {
			return err
		}
		return enqueue(
			transaction, "application_build_manifest", rebasedID.String(), "workbench.bundle_rebased",
			"worksflow.workbench.bundle.rebased", map[string]any{
				"projectId": parent.ProjectID.String(), "bundleId": rebasedID.String(),
				"parentBuildManifestId": parent.ID.String(), "rootBuildManifestId": rootID.String(),
				"workspaceRevisionId": workspaceRevision.ID.String(),
			},
		)
	})
	if err != nil {
		for _, contentID := range pendingContentIDs {
			_ = s.contents.Abort(context.Background(), contentID)
		}
		return WorkbenchBundle{}, err
	}
	var finalizeErrors []error
	for _, contentID := range pendingContentIDs {
		if err := s.contents.Finalize(ctx, contentID); err != nil {
			finalizeErrors = append(finalizeErrors, err)
		}
	}
	if err := errors.Join(finalizeErrors...); err != nil {
		return WorkbenchBundle{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return result, nil
}

func (s *WorkbenchService) stageManifestProposalsStale(
	ctx context.Context,
	transaction *gorm.DB,
	manifest storage.ApplicationBuildManifestModel,
	actorID uuid.UUID,
	staledAt time.Time,
) ([]string, error) {
	var models []storage.ImplementationProposalModel
	if err := transaction.Where(
		"project_id = ? AND build_manifest_id = ? AND status NOT IN ?",
		manifest.ProjectID, manifest.ID, []string{"stale", "applied", "partially_applied"},
	).Order("created_at ASC, id ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	pendingContentIDs := make([]string, 0, len(models))
	for _, model := range models {
		stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
		if err != nil {
			return pendingContentIDs, err
		}
		var proposal ImplementationProposal
		if err := json.Unmarshal(stored.Payload, &proposal); err != nil {
			return pendingContentIDs, err
		}
		payloadHash, err := implementationPayloadHash(proposal)
		if err != nil || payloadHash != proposal.PayloadHash || payloadHash != model.PayloadHash ||
			proposal.ID != model.ID.String() || proposal.ProjectID != model.ProjectID.String() ||
			proposal.BuildManifestID != model.BuildManifestID.String() || proposal.Status != model.Status ||
			proposal.Version != model.Version {
			return pendingContentIDs, ErrConflict
		}
		proposal.Status = "stale"
		proposal.Version++
		proposal.AppliedAt = nil
		payload, err := json.Marshal(proposal)
		if err != nil {
			return pendingContentIDs, err
		}
		contentRef, err := s.contents.PutPending(
			ctx, model.ProjectID.String(), "implementation_proposal", model.ID.String(), 1, payload,
		)
		if err != nil {
			return pendingContentIDs, err
		}
		pendingContentIDs = append(pendingContentIDs, contentRef.ID)
		updated := transaction.Model(&storage.ImplementationProposalModel{}).
			Where("id = ? AND version = ? AND status = ?", model.ID, model.Version, model.Status).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version,
				"content_ref": contentRef.ID, "content_hash": contentRef.ContentHash,
			})
		if updated.Error != nil {
			return pendingContentIDs, updated.Error
		}
		if updated.RowsAffected != 1 {
			return pendingContentIDs, ErrConflict
		}
		metadata := map[string]any{
			"buildManifestId": manifest.ID.String(), "reason": "build_manifest_rebased",
			"staledAt": staledAt.Format(time.RFC3339Nano),
		}
		if err := insertAudit(
			transaction, manifest.ProjectID, actorID, "implementation.proposal_stale",
			"implementation_proposal", model.ID.String(), metadata,
		); err != nil {
			return pendingContentIDs, err
		}
		if err := enqueue(
			transaction, "implementation_proposal", model.ID.String(), "implementation.proposal_stale",
			"worksflow.implementation.proposal.stale", map[string]any{
				"projectId": manifest.ProjectID.String(), "proposalId": model.ID.String(),
				"buildManifestId": manifest.ID.String(), "reason": "build_manifest_rebased",
			},
		); err != nil {
			return pendingContentIDs, err
		}
	}
	return pendingContentIDs, nil
}

func deriveWorkbenchBundle(
	parent WorkbenchBundle,
	bundleID uuid.UUID,
	rootID uuid.UUID,
	parentID uuid.UUID,
	workspaceRevision VersionRef,
	actorID string,
	createdAt time.Time,
) (WorkbenchBundle, error) {
	derivedFromID := parentID.String()
	result := parent
	result.ID = bundleID.String()
	result.RootBuildManifestID = rootID.String()
	result.DerivedFromBuildManifestID = &derivedFromID
	result.CurrentWorkspaceRevision = cloneVersionRef(&workspaceRevision)
	result.WorkflowContext = cloneApplicationBuildContext(parent.WorkflowContext)
	result.CreatedBy = actorID
	result.CreatedAt = createdAt
	result.ManifestHash = ""
	manifestHash, err := workbenchBundleHash(result)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	result.ManifestHash = manifestHash
	return result, nil
}

func (s *WorkbenchService) loadBundleContent(ctx context.Context, model storage.ApplicationBuildManifestModel) (WorkbenchBundle, error) {
	stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	var bundle WorkbenchBundle
	if err := json.Unmarshal(stored.Payload, &bundle); err != nil {
		return WorkbenchBundle{}, err
	}
	hash, err := workbenchBundleHash(bundle)
	if err != nil || hash != model.ManifestHash || hash != bundle.ManifestHash {
		return WorkbenchBundle{}, ErrConflict
	}
	if bundle.ID != model.ID.String() || bundle.ProjectID != model.ProjectID.String() {
		return WorkbenchBundle{}, ErrConflict
	}
	if model.WorkflowRunID == nil {
		if bundle.WorkflowRunID != nil {
			return WorkbenchBundle{}, ErrConflict
		}
	} else if !sameWorkflowRunID(bundle.WorkflowRunID, *model.WorkflowRunID) {
		return WorkbenchBundle{}, ErrConflict
	}
	if model.WorkflowRunID != nil && bundle.WorkflowContext != nil {
		var run storage.WorkflowRunModel
		if err := s.database.WithContext(ctx).Where("id = ?", *model.WorkflowRunID).Take(&run).Error; err != nil {
			return WorkbenchBundle{}, err
		}
		if err := s.validateApplicationBuildContextForRun(ctx, run, bundle.DeliverySliceID, *bundle.WorkflowContext); err != nil {
			return WorkbenchBundle{}, err
		}
	}
	legacyUnpinnedSlice := model.DeliverySliceID == nil &&
		(model.WorkflowRunID == nil || model.ManifestGroupKey != nil && *model.ManifestGroupKey == "legacy")
	if !legacyUnpinnedSlice && !optionalStringsEqual(bundle.DeliverySliceID, model.DeliverySliceID) {
		return WorkbenchBundle{}, ErrConflict
	}
	if bundle.RootBuildManifestID != "" && bundle.RootBuildManifestID != model.RootManifestID.String() {
		return WorkbenchBundle{}, ErrConflict
	}
	legacyManifestGroup := model.ManifestGroupKey != nil && *model.ManifestGroupKey == "legacy" && bundle.ManifestGroupKey == nil
	if !legacyManifestGroup && !optionalStringsEqual(bundle.ManifestGroupKey, model.ManifestGroupKey) {
		return WorkbenchBundle{}, ErrConflict
	}
	if model.DerivedFromID != nil {
		if bundle.DerivedFromBuildManifestID == nil || *bundle.DerivedFromBuildManifestID != model.DerivedFromID.String() {
			return WorkbenchBundle{}, ErrConflict
		}
	} else if bundle.DerivedFromBuildManifestID != nil {
		return WorkbenchBundle{}, ErrConflict
	}
	if model.WorkspaceRevisionID != nil {
		if bundle.CurrentWorkspaceRevision == nil || bundle.CurrentWorkspaceRevision.RevisionID != model.WorkspaceRevisionID.String() {
			return WorkbenchBundle{}, ErrConflict
		}
	}
	return bundle, nil
}

func validateCurrentWorkspaceRevision(
	transaction *gorm.DB,
	projectID uuid.UUID,
	reference VersionRef,
) (storage.ArtifactModel, storage.ArtifactRevisionModel, error) {
	artifactID, err := uuid.Parse(strings.TrimSpace(reference.ArtifactID))
	if err != nil {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, fmt.Errorf("%w: workspace artifact id", ErrInvalidInput)
	}
	revisionID, err := uuid.Parse(strings.TrimSpace(reference.RevisionID))
	if err != nil || !domain.IsCanonicalHash(reference.ContentHash) {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, fmt.Errorf("%w: workspace revision", ErrInvalidInput)
	}
	var artifact storage.ArtifactModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"id = ? AND project_id = ? AND kind = 'workspace' AND lifecycle = 'active'", artifactID, projectID,
	).Take(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, ErrNotFound
		}
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, err
	}
	if artifact.LatestApprovedRevisionID == nil || *artifact.LatestApprovedRevisionID != revisionID {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, ErrProposalStale
	}
	var revision storage.ArtifactRevisionModel
	if err := transaction.Where(
		"id = ? AND artifact_id = ? AND content_hash = ? AND workflow_status = 'approved'",
		revisionID, artifact.ID, reference.ContentHash,
	).Take(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, ErrConflict
		}
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, err
	}
	return artifact, revision, nil
}

func workspaceRevisionDescendsFrom(
	database *gorm.DB,
	artifactID uuid.UUID,
	revisionID uuid.UUID,
	ancestorID uuid.UUID,
) (bool, error) {
	var count int64
	err := database.Raw(`
WITH RECURSIVE lineage(id, parent_revision_id) AS (
  SELECT id, parent_revision_id
  FROM artifact_revisions
  WHERE id = ? AND artifact_id = ?
  UNION
  SELECT parent.id, parent.parent_revision_id
  FROM artifact_revisions AS parent
  JOIN lineage AS child ON child.parent_revision_id = parent.id
  WHERE parent.artifact_id = ?
)
SELECT count(*) FROM lineage WHERE id = ?`, revisionID, artifactID, artifactID, ancestorID).Scan(&count).Error
	return count == 1, err
}

type classifiedRefs struct {
	requirements  []VersionRef
	blueprints    []VersionRef
	pageSpecs     []VersionRef
	contracts     []VersionRef
	designSystems []VersionRef
	contexts      []WorkbenchContextRevision
}

func (s *WorkbenchService) classifyAndValidateRefs(ctx context.Context, projectID uuid.UUID, refs []VersionRef) (classifiedRefs, error) {
	result := classifiedRefs{}
	for _, ref := range refs {
		artifactID, revisionID, err := s.trace.validateRef(ctx, projectID, ref)
		if err != nil {
			return result, err
		}
		var artifact storage.ArtifactModel
		if err := s.database.WithContext(ctx).Where("id = ?", artifactID).Take(&artifact).Error; err != nil {
			return result, err
		}
		var revision storage.ArtifactRevisionModel
		if err := s.database.WithContext(ctx).Where("id = ?", revisionID).Take(&revision).Error; err != nil {
			return result, err
		}
		if revision.WorkflowStatus != "approved" {
			return result, fmt.Errorf("%w: %s revision is not approved", ErrBlockingGate, artifact.Kind)
		}
		switch artifact.Kind {
		case "project_brief", "product_requirements", "requirement_baseline":
			result.requirements = appendUniqueRef(result.requirements, ref)
		case "blueprint":
			result.blueprints = appendUniqueRef(result.blueprints, ref)
		case "page_spec":
			result.pageSpecs = appendUniqueRef(result.pageSpecs, ref)
		case "api_contract", "data_contract", "permission_contract":
			result.contracts = appendUniqueRef(result.contracts, ref)
		case "design_system", "token_set", "component_registry":
			result.designSystems = appendUniqueRef(result.designSystems, ref)
		case "decision_record", "glossary_policy", "reference_source", "change_request", "prototype_flow", "fixture_bundle":
			result.contexts = appendUniqueWorkbenchContext(result.contexts, WorkbenchContextRevision{Kind: artifact.Kind, Revision: ref})
		default:
			return result, fmt.Errorf("%w: unsupported Workbench context artifact kind %q", ErrBlockingGate, artifact.Kind)
		}
	}
	return result, nil
}

func (s *WorkbenchService) collectUpstream(ctx context.Context, projectID, startRevision uuid.UUID) ([]VersionRef, error) {
	queue := []uuid.UUID{startRevision}
	visited := map[uuid.UUID]bool{}
	refs := []VersionRef{}
	limit := s.maxUpstreamRevisions
	if limit <= 0 {
		limit = 10_000
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if visited[current] {
			continue
		}
		if len(visited) >= limit {
			return nil, fmt.Errorf("%w: Workbench upstream dependency graph exceeds %d revisions", ErrBlockingGate, limit)
		}
		visited[current] = true
		var dependencies []storage.ArtifactDependencyModel
		if err := s.database.WithContext(ctx).
			Where("project_id = ? AND target_revision_id = ?", projectID, current).
			Find(&dependencies).Error; err != nil {
			return nil, err
		}
		for _, dependency := range dependencies {
			ref := VersionRef{
				ArtifactID: dependency.SourceArtifactID.String(), RevisionID: dependency.SourceRevisionID.String(),
				ContentHash: dependency.SourceContentHash,
			}
			refs = appendUniqueRef(refs, ref)
			queue = append(queue, dependency.SourceRevisionID)
		}
	}
	return refs, nil
}

type frozenWorkbenchSource struct {
	Ref      VersionRef
	Purpose  string
	Required bool
}

func (s *WorkbenchService) collectFrozenRevisionSources(
	ctx context.Context,
	projectID uuid.UUID,
	startRevision uuid.UUID,
) ([]frozenWorkbenchSource, error) {
	frontier := []uuid.UUID{startRevision}
	visited := map[uuid.UUID]bool{}
	seenSources := map[string]bool{}
	identityHashes := map[string]string{}
	result := make([]frozenWorkbenchSource, 0)
	limit := s.maxUpstreamRevisions
	if limit <= 0 {
		limit = 10_000
	}
	for len(frontier) > 0 {
		frontier = stableUniqueApprovalUUIDs(frontier)
		unvisited := make([]uuid.UUID, 0, len(frontier))
		for _, revisionID := range frontier {
			if visited[revisionID] {
				continue
			}
			if len(visited) >= limit {
				return nil, fmt.Errorf("%w: Workbench frozen source graph exceeds %d revisions", ErrBlockingGate, limit)
			}
			visited[revisionID] = true
			unvisited = append(unvisited, revisionID)
		}
		if len(unvisited) == 0 {
			break
		}
		var sources []storage.ArtifactRevisionSourceModel
		if err := s.database.WithContext(ctx).
			Where("revision_id IN ?", unvisited).
			Order("revision_id ASC, ordinal ASC, source_revision_id ASC, purpose ASC").
			Find(&sources).Error; err != nil {
			return nil, err
		}
		next := make([]uuid.UUID, 0, len(sources))
		for _, source := range sources {
			purpose := strings.TrimSpace(source.Purpose)
			if source.SourceArtifactID == uuid.Nil || source.SourceRevisionID == uuid.Nil ||
				strings.TrimSpace(source.SourceContentHash) == "" || purpose == "" {
				return nil, fmt.Errorf("%w: Workbench frozen revision source is incomplete", ErrBlockingGate)
			}
			ref := VersionRef{
				ArtifactID: source.SourceArtifactID.String(), RevisionID: source.SourceRevisionID.String(),
				ContentHash: source.SourceContentHash, AnchorID: cloneStringPointer(source.SourceAnchorID),
			}
			identityKey := ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + dereferenceString(ref.AnchorID)
			if frozenHash, exists := identityHashes[identityKey]; exists && frozenHash != ref.ContentHash {
				return nil, fmt.Errorf("%w: Workbench frozen source identity has conflicting content hashes", ErrBlockingGate)
			}
			identityHashes[identityKey] = ref.ContentHash
			key := ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" +
				dereferenceString(ref.AnchorID) + "\x00" + purpose + "\x00" + fmt.Sprintf("%t", source.Required)
			if !seenSources[key] {
				if len(result) >= limit {
					return nil, fmt.Errorf("%w: Workbench frozen source graph exceeds %d source records", ErrBlockingGate, limit)
				}
				seenSources[key] = true
				result = append(result, frozenWorkbenchSource{Ref: ref, Purpose: purpose, Required: source.Required})
			}
			next = append(next, source.SourceRevisionID)
		}
		frontier = next
	}
	sort.Slice(result, func(left, right int) bool {
		leftValue, rightValue := result[left], result[right]
		leftKey := leftValue.Ref.ArtifactID + "\x00" + leftValue.Ref.RevisionID + "\x00" +
			dereferenceString(leftValue.Ref.AnchorID) + "\x00" + leftValue.Purpose + "\x00" +
			fmt.Sprintf("%t", leftValue.Required)
		rightKey := rightValue.Ref.ArtifactID + "\x00" + rightValue.Ref.RevisionID + "\x00" +
			dereferenceString(rightValue.Ref.AnchorID) + "\x00" + rightValue.Purpose + "\x00" +
			fmt.Sprintf("%t", rightValue.Required)
		return leftKey < rightKey
	})
	for _, source := range result {
		if _, _, err := s.trace.validateRef(ctx, projectID, source.Ref); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func frozenWorkbenchSourceRefs(sources []frozenWorkbenchSource) []VersionRef {
	refs := make([]VersionRef, 0, len(sources))
	for _, source := range sources {
		refs = appendUniqueRef(refs, source.Ref)
	}
	return refs
}

func hasFrozenPrototypePageSpecSource(sources []frozenWorkbenchSource, ref VersionRef) bool {
	for _, source := range sources {
		if source.Required && prototypePageSpecLineagePurpose(source.Purpose) &&
			exactWorkbenchVersionRef(source.Ref, ref) {
			return true
		}
	}
	return false
}

func appendUniqueWorkbenchContext(
	values []WorkbenchContextRevision,
	candidate WorkbenchContextRevision,
) []WorkbenchContextRevision {
	for _, value := range values {
		if value.Kind == candidate.Kind && exactWorkbenchVersionRef(value.Revision, candidate.Revision) {
			return values
		}
	}
	return append(values, candidate)
}

func (s *WorkbenchService) latestApprovedRefByKind(ctx context.Context, projectID uuid.UUID, kind string) (*VersionRef, error) {
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).
		Where("project_id = ? AND kind = ? AND latest_approved_revision_id IS NOT NULL", projectID, kind).
		Order("updated_at DESC").Take(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ?", *artifact.LatestApprovedRevisionID).Take(&revision).Error; err != nil {
		return nil, err
	}
	return &VersionRef{ArtifactID: artifact.ID.String(), RevisionID: revision.ID.String(), ContentHash: revision.ContentHash}, nil
}

func workbenchBundleHash(bundle WorkbenchBundle) (string, error) {
	bundle.ManifestHash = ""
	return domain.CanonicalHash(bundle)
}

func fragmentAsset(revision storage.ArtifactRevisionModel, source map[string]any, primary, fallback, name string) AssetRef {
	value, exists := source[primary]
	if !exists {
		value = source[fallback]
	}
	return valueAsset(revision, value, name, primary)
}

func valueAsset(revision storage.ArtifactRevisionModel, value any, name, fragment string) AssetRef {
	payload, _ := json.Marshal(value)
	hash, _ := domain.CanonicalHash(value)
	return AssetRef{
		AssetID: revision.ContentRef + "#" + fragment, ContentHash: hash,
		MediaType: "application/json", ByteSize: int64(len(payload)), Name: name,
	}
}

func (s *WorkbenchService) renderedFrameAssets(
	ctx context.Context,
	projectID string,
	bundleID uuid.UUID,
	revision storage.ArtifactRevisionModel,
	prototype map[string]any,
) ([]RenderedFrameRef, []string, error) {
	frames := objectSlice(prototype["frames"])
	if len(frames) == 0 {
		return nil, nil, fmt.Errorf("%w: prototype has no renderable frames", ErrBlockingGate)
	}
	breakpoints := map[string]map[string]any{}
	for _, breakpoint := range objectSlice(prototype["breakpoints"]) {
		breakpoints[firstString(breakpoint, "id")] = breakpoint
	}
	states := map[string]map[string]any{}
	for _, state := range objectSlice(prototype["states"]) {
		states[firstString(state, "id")] = state
	}
	layers := prototypeCanonicalLayerObjects(prototype)
	results := make([]RenderedFrameRef, 0, len(frames))
	pending := make([]string, 0, len(frames))
	for _, frame := range frames {
		frameID := firstString(frame, "id")
		stateID := firstString(frame, "stateId")
		breakpointID := firstString(frame, "breakpointId")
		rootLayerID := firstString(frame, "rootLayerId")
		if frameID == "" || stateID == "" || breakpointID == "" || rootLayerID == "" ||
			breakpoints[breakpointID] == nil || states[stateID] == nil || layers[rootLayerID] == nil {
			for _, contentID := range pending {
				_ = s.contents.Abort(context.Background(), contentID)
			}
			return nil, nil, fmt.Errorf("%w: prototype frame references are invalid", ErrBlockingGate)
		}
		svg := renderPrototypeFrameSVG(frame, breakpoints[breakpointID], states[stateID], prototype)
		envelope, _ := json.Marshal(map[string]any{
			"mediaType": "image/svg+xml", "encoding": "utf-8", "data": svg,
			"prototypeRevisionId": revision.ID.String(), "frameId": frameID,
		})
		assetID := bundleID.String() + ":frame:" + frameID
		contentRef, err := s.contents.PutPending(ctx, projectID, "rendered_frame", assetID, 1, envelope)
		if err != nil {
			for _, contentID := range pending {
				_ = s.contents.Abort(context.Background(), contentID)
			}
			return nil, nil, err
		}
		pending = append(pending, contentRef.ID)
		results = append(results, RenderedFrameRef{
			AssetRef: AssetRef{
				AssetID: contentRef.ID, ContentHash: contentRef.ContentHash,
				MediaType: "image/svg+xml", ByteSize: int64(len(svg)), Name: frameID + ".svg",
			},
			StateID: stateID, BreakpointID: breakpointID,
		})
	}
	return results, pending, nil
}

func renderPrototypeFrameSVG(frame, breakpoint, state, prototype map[string]any) string {
	width := intNumber(breakpoint["viewportWidth"], 1440)
	height := intNumber(breakpoint["viewportHeight"], 900)
	if width < 240 {
		width = 240
	}
	if height < 240 {
		height = 240
	}
	var builder strings.Builder
	builder.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="`)
	builder.WriteString(strconv.Itoa(width))
	builder.WriteString(`" height="`)
	builder.WriteString(strconv.Itoa(height))
	builder.WriteString(`" viewBox="0 0 `)
	builder.WriteString(strconv.Itoa(width))
	builder.WriteByte(' ')
	builder.WriteString(strconv.Itoa(height))
	builder.WriteString(`"><rect width="100%" height="100%" fill="#111114"/>`)
	builder.WriteString(`<text x="24" y="34" fill="#f5f5f5" font-family="sans-serif" font-size="18">`)
	builder.WriteString(html.EscapeString(firstString(frame, "title")))
	builder.WriteString(` · `)
	builder.WriteString(html.EscapeString(firstString(state, "title", "key", "id")))
	builder.WriteString(`</text>`)
	layersByID := prototypeCanonicalLayerObjects(prototype)
	layerIDs := reachablePrototypeLayerIDs(firstString(frame, "rootLayerId"), layersByID)
	sort.Strings(layerIDs)
	for index, id := range layerIDs {
		layer := layersByID[id]
		layout, _ := layer["layout"].(map[string]any)
		x := intNumber(layout["x"], 24+(index%6)*120)
		y := intNumber(layout["y"], 60+(index/6)*72)
		layerWidth := intNumber(layout["width"], 104)
		layerHeight := intNumber(layout["height"], 48)
		if x < 0 || y < 0 || x >= width || y >= height {
			continue
		}
		builder.WriteString(`<g><rect x="`)
		builder.WriteString(strconv.Itoa(x))
		builder.WriteString(`" y="`)
		builder.WriteString(strconv.Itoa(y))
		builder.WriteString(`" width="`)
		builder.WriteString(strconv.Itoa(min(layerWidth, width-x)))
		builder.WriteString(`" height="`)
		builder.WriteString(strconv.Itoa(min(layerHeight, height-y)))
		builder.WriteString(`" rx="6" fill="#25252b" stroke="#53535f"/><text x="`)
		builder.WriteString(strconv.Itoa(x + 8))
		builder.WriteString(`" y="`)
		builder.WriteString(strconv.Itoa(y + min(layerHeight, 48)/2 + 5))
		builder.WriteString(`" fill="#d7d7df" font-family="sans-serif" font-size="12">`)
		builder.WriteString(html.EscapeString(firstString(layer, "name", "id")))
		builder.WriteString(`</text></g>`)
	}
	builder.WriteString(`</svg>`)
	return builder.String()
}

func prototypeLayerObjects(value any) map[string]map[string]any {
	if object, ok := value.(map[string]any); ok {
		result := map[string]map[string]any{}
		for id, item := range object {
			if layer, ok := item.(map[string]any); ok {
				result[id] = layer
			}
		}
		return result
	}
	result := map[string]map[string]any{}
	for _, layer := range objectSlice(value) {
		if id := firstString(layer, "id", "layerId"); id != "" {
			result[id] = layer
		}
	}
	return result
}

func prototypeCanonicalLayerObjects(prototype map[string]any) map[string]map[string]any {
	layers := prototypeLayerObjects(prototype["layers"])
	if len(layers) > 0 {
		return layers
	}
	if scene, ok := prototype["scene"].(map[string]any); ok {
		return prototypeLayerObjects(scene["layers"])
	}
	return layers
}

func reachablePrototypeLayerIDs(rootLayerID string, layers map[string]map[string]any) []string {
	rootLayerID = strings.TrimSpace(rootLayerID)
	if rootLayerID == "" || layers[rootLayerID] == nil {
		return nil
	}
	visited := map[string]bool{}
	queue := []string{rootLayerID}
	result := make([]string, 0, len(layers))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if visited[current] || layers[current] == nil {
			continue
		}
		visited[current] = true
		result = append(result, current)
		children := append([]string(nil), stringSlice(layers[current]["childIds"])...)
		sort.Strings(children)
		queue = append(queue, children...)
	}
	return result
}

func intNumber(value any, fallback int) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case json.Number:
		if parsed, err := strconv.Atoi(number.String()); err == nil {
			return parsed
		}
	}
	return fallback
}

func versionRefFromValue(value any) (VersionRef, bool) {
	reference, ok := value.(map[string]any)
	if !ok {
		return VersionRef{}, false
	}
	result := VersionRef{
		ArtifactID: firstString(reference, "artifactId"), RevisionID: firstString(reference, "revisionId"),
		ContentHash: firstString(reference, "contentHash"),
	}
	return result, result.ArtifactID != "" && result.RevisionID != "" && result.ContentHash != ""
}

func appendUniqueRef(values []VersionRef, value VersionRef) []VersionRef {
	for _, existing := range values {
		if existing.ArtifactID == value.ArtifactID && existing.RevisionID == value.RevisionID && stringPointerEqual(existing.AnchorID, value.AnchorID) {
			return values
		}
	}
	return append(values, value)
}

func sortVersionRefs(values []VersionRef) {
	sort.Slice(values, func(left, right int) bool {
		return values[left].ArtifactID+values[left].RevisionID < values[right].ArtifactID+values[right].RevisionID
	})
}
