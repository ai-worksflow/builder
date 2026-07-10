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
	ID                       string             `json:"id"`
	ProjectID                string             `json:"projectId"`
	WorkflowRunID            *string            `json:"workflowRunId,omitempty"`
	DeliverySliceID          *string            `json:"deliverySliceId,omitempty"`
	PageSpecRevision         VersionRef         `json:"pageSpecRevision"`
	PrototypeRevision        VersionRef         `json:"prototypeRevision"`
	RequirementRevisions     []VersionRef       `json:"requirementRevisions"`
	BlueprintRevision        VersionRef         `json:"blueprintRevision"`
	ContractRevisions        []VersionRef       `json:"contractRevisions"`
	DesignSystemRevisions    []VersionRef       `json:"designSystemRevisions"`
	CurrentWorkspaceRevision *VersionRef        `json:"currentWorkspaceRevision,omitempty"`
	SceneGraph               AssetRef           `json:"sceneGraph"`
	RenderedFrames           []RenderedFrameRef `json:"renderedFrames"`
	InteractionManifest      AssetRef           `json:"interactionManifest"`
	FixtureBundle            AssetRef           `json:"fixtureBundle"`
	TokenManifest            AssetRef           `json:"tokenManifest"`
	ComponentMapping         AssetRef           `json:"componentMapping"`
	TraceMatrix              AssetRef           `json:"traceMatrix"`
	AcceptanceManifest       AssetRef           `json:"acceptanceManifest"`
	Assumptions              []string           `json:"assumptions"`
	Waivers                  []string           `json:"waivers"`
	CreatedBy                string             `json:"createdBy"`
	CreatedAt                time.Time          `json:"createdAt"`
	ManifestHash             string             `json:"contentHash"`
}

type CreateWorkbenchBundleInput struct {
	PrototypeRevision VersionRef `json:"prototypeRevision"`
	WorkflowRunID     *string    `json:"workflowRunId,omitempty"`
	DeliverySliceID   *string    `json:"deliverySliceId,omitempty"`
	AllowStale        bool       `json:"allowStale,omitempty"`
	OverrideReason    string     `json:"overrideReason,omitempty"`
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
		ID: bundleID.String(), ProjectID: projectID, WorkflowRunID: input.WorkflowRunID,
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
	var workflowRunID *uuid.UUID
	if input.WorkflowRunID != nil {
		parsed, err := uuid.Parse(*input.WorkflowRunID)
		if err != nil {
			return WorkbenchBundle{}, fmt.Errorf("%w: workflow run id", ErrInvalidInput)
		}
		workflowRunID = &parsed
	}
	model := storage.ApplicationBuildManifestModel{
		ID: bundleID, ProjectID: projectUUID, WorkflowRunID: workflowRunID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: contentRef.ID,
		ContentHash: contentRef.ContentHash, ManifestHash: bundle.ManifestHash,
		Status: "frozen", CreatedBy: actorUUID, CreatedAt: now,
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
	return bundle, nil
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
