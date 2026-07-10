package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

const (
	BlueprintSelectionJobType     = "blueprint.selection"
	SelectionDocumentationJobType = "selection.documentation"
)

type BlueprintSelectionInput struct {
	BlueprintRevision VersionRef `json:"blueprintRevision"`
	NodeIDs           []string   `json:"nodeIds"`
}

type blueprintSelectionNode struct {
	ID             string   `json:"id"`
	Key            string   `json:"key"`
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	RequirementIDs []string `json:"requirementIds,omitempty"`
}

type blueprintSelectionEdge struct {
	ID       string `json:"id"`
	SourceID string `json:"sourceNodeId"`
	TargetID string `json:"targetNodeId"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
}

type BlueprintSelectionPageBinding struct {
	NodeID    string      `json:"nodeId"`
	PageSpec  *VersionRef `json:"pageSpec,omitempty"`
	Prototype *VersionRef `json:"prototype,omitempty"`
}

type BlueprintSelectionScope struct {
	SchemaVersion int                             `json:"schemaVersion"`
	SelectionID   string                          `json:"selectionId"`
	Blueprint     VersionRef                      `json:"blueprint"`
	NodeIDs       []string                        `json:"nodeIds"`
	Nodes         []blueprintSelectionNode        `json:"nodes"`
	Edges         []blueprintSelectionEdge        `json:"edges"`
	PageBindings  []BlueprintSelectionPageBinding `json:"pageBindings"`
}

func (s *ProposalService) compileBlueprintSelection(
	ctx context.Context,
	projectID uuid.UUID,
	input CreateManifestInput,
) (CreateManifestInput, error) {
	selection := input.BlueprintSelection
	if selection == nil || strings.TrimSpace(input.ExpectedBlueprintETag) == "" {
		return CreateManifestInput{}, fmt.Errorf("%w: blueprint selection precondition", ErrInvalidInput)
	}
	if strings.TrimSpace(dereferenceString(selection.BlueprintRevision.AnchorID)) != "" {
		return CreateManifestInput{}, fmt.Errorf("%w: blueprint selection revision must be a whole-artifact reference", ErrInvalidInput)
	}
	if len(selection.NodeIDs) == 0 || len(selection.NodeIDs) > 100 {
		return CreateManifestInput{}, fmt.Errorf("%w: blueprint selection requires 1 to 100 nodes", ErrInvalidInput)
	}
	artifactID, revisionID, err := s.trace.validateRef(ctx, projectID, selection.BlueprintRevision)
	if err != nil {
		return CreateManifestInput{}, err
	}
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND project_id = ? AND kind = 'blueprint' AND lifecycle = 'active'",
		artifactID, projectID,
	).Take(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CreateManifestInput{}, ErrNotFound
		}
		return CreateManifestInput{}, err
	}
	if artifactETag(artifact.ID, artifact.Version) != strings.TrimSpace(input.ExpectedBlueprintETag) {
		return CreateManifestInput{}, ErrConflict
	}
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND artifact_id = ? AND content_hash = ? AND workflow_status = 'approved'",
		revisionID, artifactID, selection.BlueprintRevision.ContentHash,
	).Take(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CreateManifestInput{}, fmt.Errorf("%w: Blueprint selection requires an approved revision", ErrBlockingGate)
		}
		return CreateManifestInput{}, err
	}
	stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
	if err != nil {
		return CreateManifestInput{}, err
	}
	nodes, edges, err := decodeBlueprintSelectionGraph(stored.Payload)
	if err != nil {
		return CreateManifestInput{}, err
	}
	requested := make(map[string]struct{}, len(selection.NodeIDs))
	for _, rawID := range selection.NodeIDs {
		nodeID := strings.TrimSpace(rawID)
		if nodeID == "" {
			return CreateManifestInput{}, fmt.Errorf("%w: blueprint selection node id", ErrInvalidInput)
		}
		if _, duplicate := requested[nodeID]; duplicate {
			return CreateManifestInput{}, fmt.Errorf("%w: duplicate blueprint selection node", ErrInvalidInput)
		}
		requested[nodeID] = struct{}{}
	}
	selectedNodes := make([]blueprintSelectionNode, 0, len(requested))
	for _, node := range nodes {
		if _, selected := requested[node.ID]; !selected {
			continue
		}
		selectedNodes = append(selectedNodes, node)
		delete(requested, node.ID)
	}
	if len(requested) != 0 {
		return CreateManifestInput{}, fmt.Errorf("%w: selected node is not an exact Blueprint anchor", ErrNotFound)
	}
	sort.Slice(selectedNodes, func(i, j int) bool { return selectedNodes[i].ID < selectedNodes[j].ID })
	nodeIDs := make([]string, len(selectedNodes))
	selectedSet := make(map[string]bool, len(selectedNodes))
	for index, node := range selectedNodes {
		nodeIDs[index] = node.ID
		selectedSet[node.ID] = true
	}
	selectedEdges := make([]blueprintSelectionEdge, 0)
	for _, edge := range edges {
		if selectedSet[edge.SourceID] && selectedSet[edge.TargetID] {
			selectedEdges = append(selectedEdges, edge)
		}
	}
	sort.Slice(selectedEdges, func(i, j int) bool { return selectedEdges[i].ID < selectedEdges[j].ID })

	sources := make([]ManifestSourceInput, 0, len(selectedNodes)*3+4)
	seenSources := map[string]bool{}
	appendSource := func(ref VersionRef, purpose string) {
		anchor := dereferenceString(ref.AnchorID)
		key := ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + anchor
		if seenSources[key] {
			return
		}
		seenSources[key] = true
		sources = append(sources, ManifestSourceInput{Ref: ref, Purpose: purpose})
	}
	root := selection.BlueprintRevision
	root.AnchorID = nil
	appendSource(root, "blueprint_selection_root")
	for _, node := range selectedNodes {
		anchor := node.ID
		ref := root
		ref.AnchorID = &anchor
		appendSource(ref, "blueprint_selection_node")
	}

	pageBindings := make([]BlueprintSelectionPageBinding, 0)
	for _, node := range selectedNodes {
		if node.Kind != "page" {
			continue
		}
		binding, err := s.resolveBlueprintSelectionPageBinding(ctx, projectID, revision.ID, node.ID)
		if err != nil {
			return CreateManifestInput{}, err
		}
		pageBindings = append(pageBindings, binding)
		if binding.PageSpec != nil {
			appendSource(*binding.PageSpec, "selected_page_spec")
		}
		if binding.Prototype != nil {
			appendSource(*binding.Prototype, "selected_prototype")
		}
	}
	contextRefs, err := s.blueprintSelectionContextRefs(ctx, projectID, revision.ID)
	if err != nil {
		return CreateManifestInput{}, err
	}
	for _, ref := range contextRefs {
		appendSource(ref, "selection_context")
	}
	sort.Slice(sources, func(i, j int) bool {
		left, right := sources[i].Ref, sources[j].Ref
		return left.ArtifactID+"\x00"+left.RevisionID+"\x00"+dereferenceString(left.AnchorID) <
			right.ArtifactID+"\x00"+right.RevisionID+"\x00"+dereferenceString(right.AnchorID)
	})
	sort.Slice(pageBindings, func(i, j int) bool { return pageBindings[i].NodeID < pageBindings[j].NodeID })

	identityPayload := struct {
		Blueprint    VersionRef                      `json:"blueprint"`
		NodeIDs      []string                        `json:"nodeIds"`
		PageBindings []BlueprintSelectionPageBinding `json:"pageBindings"`
		Sources      []ManifestSourceInput           `json:"sources"`
	}{Blueprint: root, NodeIDs: nodeIDs, PageBindings: pageBindings, Sources: sources}
	selectionID, err := domain.CanonicalHash(identityPayload)
	if err != nil {
		return CreateManifestInput{}, err
	}
	scope := BlueprintSelectionScope{
		SchemaVersion: 1, SelectionID: selectionID, Blueprint: root,
		NodeIDs: nodeIDs, Nodes: selectedNodes, Edges: selectedEdges, PageBindings: pageBindings,
	}
	constraints, err := domain.CanonicalJSON(map[string]any{"blueprintSelection": scope})
	if err != nil {
		return CreateManifestInput{}, err
	}
	input.JobType = BlueprintSelectionJobType
	input.DeliverySliceID = selectionID
	input.BaseRevision = nil
	input.Sources = sources
	input.Constraints = constraints
	input.OutputSchemaVersion = "blueprint-selection/v1"
	return input, nil
}

// validateParentBlueprintSelection makes derived AI manifests prove their
// selection provenance instead of accepting a client-authored selection ID.
// The complete frozen scope and exact artifact refs must equal the immutable
// parent selection manifest.
func (s *ProposalService) validateParentBlueprintSelection(
	ctx context.Context,
	projectID uuid.UUID,
	actorID string,
	input CreateManifestInput,
) error {
	var envelope struct {
		ParentSelectionManifest *domain.ManifestRef `json:"parentSelectionManifest"`
		FrozenSelectionScope    json.RawMessage     `json:"frozenSelectionScope"`
	}
	requiresParent := input.JobType == SelectionDocumentationJobType
	if len(input.Constraints) == 0 {
		if requiresParent {
			return fmt.Errorf("%w: selection documentation requires a parent selection manifest", ErrInvalidInput)
		}
		return nil
	}
	if err := json.Unmarshal(input.Constraints, &envelope); err != nil {
		return fmt.Errorf("%w: manifest constraints", ErrInvalidInput)
	}
	if envelope.ParentSelectionManifest == nil {
		if requiresParent {
			return fmt.Errorf("%w: selection documentation requires a parent selection manifest", ErrInvalidInput)
		}
		return nil
	}
	if err := envelope.ParentSelectionManifest.Validate(); err != nil {
		return err
	}
	parent, err := s.GetManifest(ctx, envelope.ParentSelectionManifest.ID, actorID)
	if err != nil {
		return err
	}
	if parent.ProjectID != projectID.String() || parent.Ref() != *envelope.ParentSelectionManifest ||
		parent.JobType != BlueprintSelectionJobType {
		return ErrConflict
	}
	var parentEnvelope struct {
		BlueprintSelection BlueprintSelectionScope `json:"blueprintSelection"`
	}
	if err := json.Unmarshal(parent.Constraints, &parentEnvelope); err != nil {
		return fmt.Errorf("%w: parent Blueprint selection scope", ErrInvalidInput)
	}
	expectedScope, err := domain.CanonicalJSON(parentEnvelope.BlueprintSelection)
	if err != nil {
		return err
	}
	providedScope, err := domain.CanonicalJSON(envelope.FrozenSelectionScope)
	if err != nil || !bytes.Equal(expectedScope, providedScope) {
		return fmt.Errorf("%w: frozen selection scope differs from parent manifest", ErrConflict)
	}
	parentRefs := make(map[string]bool, len(parent.Sources))
	for _, source := range parent.Sources {
		parentRefs[selectionDomainRefKey(source.Ref)] = true
	}
	providedRefs := make(map[string]bool, len(input.Sources))
	for _, source := range input.Sources {
		ref := domain.ArtifactRef{
			ArtifactID: source.Ref.ArtifactID, RevisionID: source.Ref.RevisionID,
			ContentHash: source.Ref.ContentHash, AnchorID: dereferenceString(source.Ref.AnchorID),
		}
		providedRefs[selectionDomainRefKey(ref)] = true
	}
	if len(parentRefs) != len(providedRefs) {
		return fmt.Errorf("%w: derived selection sources differ from parent manifest", ErrConflict)
	}
	for key := range parentRefs {
		if !providedRefs[key] {
			return fmt.Errorf("%w: derived selection sources differ from parent manifest", ErrConflict)
		}
	}
	return nil
}

func selectionDomainRefKey(ref domain.ArtifactRef) string {
	return ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" + ref.AnchorID
}

func decodeBlueprintSelectionGraph(payload json.RawMessage) ([]blueprintSelectionNode, []blueprintSelectionEdge, error) {
	var content struct {
		Nodes    []blueprintSelectionNode `json:"nodes"`
		Edges    []blueprintSelectionEdge `json:"edges"`
		Semantic *struct {
			Nodes []blueprintSelectionNode `json:"nodes"`
			Edges []blueprintSelectionEdge `json:"edges"`
		} `json:"semantic"`
	}
	if err := json.Unmarshal(payload, &content); err != nil {
		return nil, nil, fmt.Errorf("%w: decode Blueprint selection graph", ErrInvalidInput)
	}
	if content.Semantic != nil {
		content.Nodes, content.Edges = content.Semantic.Nodes, content.Semantic.Edges
	}
	if len(content.Nodes) == 0 {
		return nil, nil, fmt.Errorf("%w: Blueprint selection graph has no semantic nodes", ErrBlockingGate)
	}
	seen := map[string]bool{}
	for index := range content.Nodes {
		node := &content.Nodes[index]
		node.ID, node.Key, node.Kind, node.Title = strings.TrimSpace(node.ID), strings.TrimSpace(node.Key), strings.TrimSpace(node.Kind), strings.TrimSpace(node.Title)
		if node.ID == "" || node.Key == "" || node.Kind == "" || node.Title == "" || seen[node.ID] {
			return nil, nil, fmt.Errorf("%w: Blueprint semantic node identity", ErrBlockingGate)
		}
		seen[node.ID] = true
	}
	return content.Nodes, content.Edges, nil
}

func (s *ProposalService) resolveBlueprintSelectionPageBinding(
	ctx context.Context,
	projectID, blueprintRevisionID uuid.UUID,
	nodeID string,
) (BlueprintSelectionPageBinding, error) {
	type revisionRow struct {
		ID          uuid.UUID
		ArtifactID  uuid.UUID
		ContentHash string
	}
	var pageSpecs []revisionRow
	if err := s.database.WithContext(ctx).Table("artifact_revision_sources AS source").
		Select("revision.id, revision.artifact_id, revision.content_hash").
		Joins("JOIN artifact_revisions AS revision ON revision.id = source.revision_id").
		Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Where("source.source_revision_id = ? AND source.source_anchor_id = ?", blueprintRevisionID, nodeID).
		Where("source.purpose = 'blueprint' AND source.required = TRUE").
		Where("artifact.project_id = ? AND artifact.kind = 'page_spec' AND artifact.lifecycle = 'active'", projectID).
		Where("artifact.latest_approved_revision_id = revision.id AND revision.workflow_status = 'approved'").
		Order("artifact.id ASC").Limit(2).Scan(&pageSpecs).Error; err != nil {
		return BlueprintSelectionPageBinding{}, err
	}
	binding := BlueprintSelectionPageBinding{NodeID: nodeID}
	if len(pageSpecs) == 0 {
		return binding, nil
	}
	if len(pageSpecs) != 1 {
		return BlueprintSelectionPageBinding{}, fmt.Errorf("%w: Blueprint page has multiple current approved PageSpecs", ErrConflict)
	}
	pageRef := VersionRef{ArtifactID: pageSpecs[0].ArtifactID.String(), RevisionID: pageSpecs[0].ID.String(), ContentHash: pageSpecs[0].ContentHash}
	binding.PageSpec = &pageRef
	var prototypes []revisionRow
	if err := s.database.WithContext(ctx).Table("artifact_revision_sources AS source").
		Select("revision.id, revision.artifact_id, revision.content_hash").
		Joins("JOIN artifact_revisions AS revision ON revision.id = source.revision_id").
		Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Where("source.source_revision_id = ? AND source.source_anchor_id IS NULL", pageSpecs[0].ID).
		Where("source.purpose = 'page_spec' AND source.required = TRUE").
		Where("artifact.project_id = ? AND artifact.kind = 'prototype' AND artifact.lifecycle = 'active'", projectID).
		Where("artifact.latest_approved_revision_id = revision.id AND revision.workflow_status = 'approved'").
		Order("artifact.id ASC").Limit(2).Scan(&prototypes).Error; err != nil {
		return BlueprintSelectionPageBinding{}, err
	}
	if len(prototypes) > 1 {
		return BlueprintSelectionPageBinding{}, fmt.Errorf("%w: PageSpec has multiple current approved Prototypes", ErrConflict)
	}
	if len(prototypes) == 1 {
		prototypeRef := VersionRef{ArtifactID: prototypes[0].ArtifactID.String(), RevisionID: prototypes[0].ID.String(), ContentHash: prototypes[0].ContentHash}
		binding.Prototype = &prototypeRef
	}
	return binding, nil
}

func (s *ProposalService) blueprintSelectionContextRefs(
	ctx context.Context,
	projectID, blueprintRevisionID uuid.UUID,
) ([]VersionRef, error) {
	type sourceRow struct {
		ArtifactID  uuid.UUID
		RevisionID  uuid.UUID
		ContentHash string
		AnchorID    *string
	}
	var rows []sourceRow
	if err := s.database.WithContext(ctx).Table("artifact_revision_sources AS source").
		Select("source.source_artifact_id AS artifact_id, source.source_revision_id AS revision_id, source.source_content_hash AS content_hash, source.source_anchor_id AS anchor_id").
		Joins("JOIN artifacts AS artifact ON artifact.id = source.source_artifact_id").
		Joins("JOIN artifact_revisions AS revision ON revision.id = source.source_revision_id AND revision.artifact_id = artifact.id").
		Where("source.revision_id = ? AND artifact.project_id = ?", blueprintRevisionID, projectID).
		Where("revision.workflow_status = 'approved'").
		Order("source.ordinal ASC, source.source_artifact_id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	refs := make([]VersionRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, VersionRef{
			ArtifactID: row.ArtifactID.String(), RevisionID: row.RevisionID.String(),
			ContentHash: row.ContentHash, AnchorID: row.AnchorID,
		})
	}
	return refs, nil
}
