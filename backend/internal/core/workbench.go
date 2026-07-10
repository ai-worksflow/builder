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
	ID                         string             `json:"id"`
	ProjectID                  string             `json:"projectId"`
	RootBuildManifestID        string             `json:"rootBuildManifestId,omitempty"`
	DerivedFromBuildManifestID *string            `json:"derivedFromBuildManifestId,omitempty"`
	WorkflowRunID              *string            `json:"workflowRunId,omitempty"`
	ManifestGroupKey           *string            `json:"manifestGroupKey,omitempty"`
	DeliverySliceID            *string            `json:"deliverySliceId,omitempty"`
	PageSpecRevision           VersionRef         `json:"pageSpecRevision"`
	PrototypeRevision          VersionRef         `json:"prototypeRevision"`
	RequirementRevisions       []VersionRef       `json:"requirementRevisions"`
	BlueprintRevision          VersionRef         `json:"blueprintRevision"`
	ContractRevisions          []VersionRef       `json:"contractRevisions"`
	DesignSystemRevisions      []VersionRef       `json:"designSystemRevisions"`
	CurrentWorkspaceRevision   *VersionRef        `json:"currentWorkspaceRevision,omitempty"`
	SceneGraph                 AssetRef           `json:"sceneGraph"`
	RenderedFrames             []RenderedFrameRef `json:"renderedFrames"`
	InteractionManifest        AssetRef           `json:"interactionManifest"`
	FixtureBundle              AssetRef           `json:"fixtureBundle"`
	TokenManifest              AssetRef           `json:"tokenManifest"`
	ComponentMapping           AssetRef           `json:"componentMapping"`
	TraceMatrix                AssetRef           `json:"traceMatrix"`
	AcceptanceManifest         AssetRef           `json:"acceptanceManifest"`
	Assumptions                []string           `json:"assumptions"`
	Waivers                    []string           `json:"waivers"`
	CreatedBy                  string             `json:"createdBy"`
	CreatedAt                  time.Time          `json:"createdAt"`
	ManifestHash               string             `json:"contentHash"`
}

type CreateWorkbenchBundleInput struct {
	PrototypeRevision VersionRef `json:"prototypeRevision"`
	WorkflowRunID     *string    `json:"workflowRunId,omitempty"`
	ManifestGroupKey  *string    `json:"manifestGroupKey,omitempty"`
	RootOrdinal       *int       `json:"rootOrdinal,omitempty"`
	DeliverySliceID   *string    `json:"deliverySliceId,omitempty"`
	AllowStale        bool       `json:"allowStale,omitempty"`
	OverrideReason    string     `json:"overrideReason,omitempty"`
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
	database *gorm.DB
	contents content.Store
	access   *AccessControl
	trace    *TraceService
	now      func() time.Time
}

func NewWorkbenchService(database *gorm.DB, contents content.Store, access *AccessControl) (*WorkbenchService, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("workbench database, content store and access control are required")
	}
	trace, _ := NewTraceService(database, access, contents)
	return &WorkbenchService{database: database, contents: contents, access: access, trace: trace, now: time.Now}, nil
}

func (s *WorkbenchService) CreateBundle(ctx context.Context, projectID, actorID string, input CreateWorkbenchBundleInput) (WorkbenchBundle, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return WorkbenchBundle{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	var workflowRunID *uuid.UUID
	if input.WorkflowRunID != nil {
		parsed, err := uuid.Parse(*input.WorkflowRunID)
		if err != nil || input.RootOrdinal == nil || *input.RootOrdinal < 0 || input.ManifestGroupKey == nil ||
			strings.TrimSpace(*input.ManifestGroupKey) == "" || len(strings.TrimSpace(*input.ManifestGroupKey)) > 200 {
			return WorkbenchBundle{}, fmt.Errorf("%w: workflow run, manifest group and root ordinal", ErrInvalidInput)
		}
		workflowRunID = &parsed
		var run storage.WorkflowRunModel
		if err := s.database.WithContext(ctx).Select("id", "project_id", "started_by").
			Where("id = ?", parsed).Take(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return WorkbenchBundle{}, ErrConflict
			}
			return WorkbenchBundle{}, err
		}
		if run.ProjectID != projectUUID || run.StartedBy != actorUUID {
			return WorkbenchBundle{}, ErrConflict
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
	formal := prototypeRevision.WorkflowStatus == "approved" && health.SyncStatus != "needs_sync" && health.SyncStatus != "blocked"
	waivers := []string{}
	if !formal {
		if !input.AllowStale || strings.TrimSpace(input.OverrideReason) == "" {
			return WorkbenchBundle{}, fmt.Errorf("%w: prototype must be approved and current", ErrBlockingGate)
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

	upstream, err := s.collectUpstream(ctx, projectUUID, prototypeRevisionID)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	if pageRef, ok := versionRefFromValue(prototype["pageSpecRevision"]); ok {
		upstream = appendUniqueRef(upstream, pageRef)
		_, pageRevisionID, validationErr := s.trace.validateRef(ctx, projectUUID, pageRef)
		if validationErr != nil {
			return WorkbenchBundle{}, validationErr
		}
		pageUpstream, collectErr := s.collectUpstream(ctx, projectUUID, pageRevisionID)
		if collectErr != nil {
			return WorkbenchBundle{}, collectErr
		}
		for _, reference := range pageUpstream {
			upstream = appendUniqueRef(upstream, reference)
		}
	}
	classified, err := s.classifyAndValidateRefs(ctx, projectUUID, upstream)
	if err != nil {
		return WorkbenchBundle{}, err
	}
	if len(classified.pageSpecs) != 1 || len(classified.blueprints) != 1 || len(classified.requirements) == 0 {
		return WorkbenchBundle{}, fmt.Errorf("%w: bundle needs one PageSpec, one Blueprint, and at least one approved requirement revision", ErrBlockingGate)
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
		!optionalStringsEqual(model.ManifestGroupKey, input.ManifestGroupKey) {
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
		if !optionalUUIDsEqual(model.WorkflowRunID, root.WorkflowRunID) {
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
	payloadHash, err := implementationPayloadHash(proposal)
	if err != nil || payloadHash != proposal.PayloadHash || payloadHash != model.PayloadHash ||
		proposal.ID != model.ID.String() || proposal.ProjectID != model.ProjectID.String() ||
		proposal.BuildManifestID != model.BuildManifestID.String() || proposal.Status != model.Status || proposal.Version != model.Version {
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
	for {
		if current.ID == uuid.Nil || current.ProjectID != projectID || visited[current.ID] {
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

		now := s.now().UTC()
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
		}
	}
	return result, nil
}

func (s *WorkbenchService) collectUpstream(ctx context.Context, projectID, startRevision uuid.UUID) ([]VersionRef, error) {
	queue := []uuid.UUID{startRevision}
	visited := map[uuid.UUID]bool{}
	refs := []VersionRef{}
	for len(queue) > 0 && len(visited) < 10_000 {
		current := queue[0]
		queue = queue[1:]
		if visited[current] {
			continue
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

func renderedFrameRefs(prototype map[string]any) []RenderedFrameRef {
	frames := objectSlice(prototype["renderedFrames"])
	result := make([]RenderedFrameRef, 0, len(frames))
	for _, frame := range frames {
		assetID := firstString(frame, "assetId")
		hash := firstString(frame, "contentHash")
		stateID := firstString(frame, "stateId")
		breakpointID := firstString(frame, "breakpointId")
		if assetID == "" || hash == "" || stateID == "" || breakpointID == "" {
			continue
		}
		result = append(result, RenderedFrameRef{
			AssetRef: AssetRef{AssetID: assetID, ContentHash: hash, MediaType: firstString(frame, "mediaType"), Name: firstString(frame, "name")},
			StateID:  stateID, BreakpointID: breakpointID,
		})
	}
	return result
}

func (s *WorkbenchService) renderedFrameAssets(
	ctx context.Context,
	projectID string,
	bundleID uuid.UUID,
	revision storage.ArtifactRevisionModel,
	prototype map[string]any,
) ([]RenderedFrameRef, []string, error) {
	if existing := renderedFrameRefs(prototype); len(existing) > 0 {
		return existing, nil, nil
	}
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
	results := make([]RenderedFrameRef, 0, len(frames))
	pending := make([]string, 0, len(frames))
	for _, frame := range frames {
		frameID := firstString(frame, "id")
		stateID := firstString(frame, "stateId")
		breakpointID := firstString(frame, "breakpointId")
		if frameID == "" || stateID == "" || breakpointID == "" || breakpoints[breakpointID] == nil || states[stateID] == nil {
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
	layersByID := prototypeLayerObjects(prototype["layers"])
	layerIDs := make([]string, 0, len(layersByID))
	for id := range layersByID {
		layerIDs = append(layerIDs, id)
	}
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
