package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

type DocumentGraph struct {
	ProjectID string              `json:"projectId"`
	Nodes     []DocumentGraphNode `json:"nodes"`
	Edges     []DocumentGraphEdge `json:"edges"`
}

type DocumentGraphNode struct {
	ID           string                  `json:"id"`
	EntityID     string                  `json:"entityId"`
	EntityType   string                  `json:"entityType"`
	ArtifactKind string                  `json:"artifactKind,omitempty"`
	Title        string                  `json:"title"`
	Status       string                  `json:"status"`
	Revision     *core.VersionRef        `json:"revision,omitempty"`
	Bindings     []DocumentMemberBinding `json:"memberBindings,omitempty"`
	Metadata     json.RawMessage         `json:"metadata"`
	UpdatedAt    time.Time               `json:"updatedAt"`
}

type DocumentGraphEdge struct {
	ID       string          `json:"id"`
	SourceID string          `json:"sourceId"`
	TargetID string          `json:"targetId"`
	Relation string          `json:"relation"`
	Required bool            `json:"required"`
	Metadata json.RawMessage `json:"metadata"`
}

func (s *Service) GetDocumentGraph(ctx context.Context, projectID, actorID string) (DocumentGraph, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return DocumentGraph{}, err
	}
	projectUUID, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return DocumentGraph{}, fmt.Errorf("%w: project id", core.ErrInvalidInput)
	}
	graph := DocumentGraph{ProjectID: projectUUID.String(), Nodes: []DocumentGraphNode{}, Edges: []DocumentGraphEdge{}}
	artifactNodes, artifactEdges, err := s.artifactGraph(ctx, projectUUID)
	if err != nil {
		return DocumentGraph{}, err
	}
	graph.Nodes = append(graph.Nodes, artifactNodes...)
	graph.Edges = append(graph.Edges, artifactEdges...)
	aiNodes, aiEdges, err := s.aiIOGraph(ctx, projectUUID)
	if err != nil {
		return DocumentGraph{}, err
	}
	graph.Nodes = append(graph.Nodes, aiNodes...)
	graph.Edges = append(graph.Edges, aiEdges...)
	implementationNodes, implementationEdges, err := s.implementationGraph(ctx, projectUUID)
	if err != nil {
		return DocumentGraph{}, err
	}
	graph.Nodes = append(graph.Nodes, implementationNodes...)
	graph.Edges = append(graph.Edges, implementationEdges...)
	deploymentNodes, deploymentEdges, err := s.deploymentGraph(ctx, projectUUID)
	if err != nil {
		return DocumentGraph{}, err
	}
	graph.Nodes = append(graph.Nodes, deploymentNodes...)
	graph.Edges = append(graph.Edges, deploymentEdges...)
	sort.Slice(graph.Nodes, func(i, j int) bool {
		if graph.Nodes[i].EntityType == graph.Nodes[j].EntityType {
			return graph.Nodes[i].ID < graph.Nodes[j].ID
		}
		return graph.Nodes[i].EntityType < graph.Nodes[j].EntityType
	})
	sort.Slice(graph.Edges, func(i, j int) bool { return graph.Edges[i].ID < graph.Edges[j].ID })
	return graph, nil
}

func (s *Service) aiIOGraph(ctx context.Context, projectID uuid.UUID) ([]DocumentGraphNode, []DocumentGraphEdge, error) {
	var manifestModels []storage.InputManifestModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectID).
		Order("created_at DESC, id DESC").Limit(1000).Find(&manifestModels).Error; err != nil {
		return nil, nil, err
	}
	manifestIDs := make([]uuid.UUID, 0, len(manifestModels))
	manifests := make(map[uuid.UUID]domain.InputManifest, len(manifestModels))
	nodes := make([]DocumentGraphNode, 0, len(manifestModels)*2)
	edges := make([]DocumentGraphEdge, 0, len(manifestModels)*4)
	for _, model := range manifestModels {
		stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
		if err != nil {
			return nil, nil, err
		}
		var manifest domain.InputManifest
		if err := json.Unmarshal(stored.Payload, &manifest); err != nil {
			return nil, nil, err
		}
		if manifest.ID != model.ID.String() || manifest.ProjectID != projectID.String() || manifest.Hash != model.ManifestHash {
			return nil, nil, core.ErrConflict
		}
		manifestIDs = append(manifestIDs, model.ID)
		manifests[model.ID] = manifest
		metadata, _ := json.Marshal(map[string]any{
			"jobType": manifest.JobType, "manifestHash": manifest.Hash, "contentHash": model.ContentHash,
			"outputSchemaVersion": manifest.OutputSchemaVersion, "sourceCount": len(manifest.Sources),
			"baseRevision": manifest.BaseRevision,
		})
		nodes = append(nodes, DocumentGraphNode{
			ID: graphNodeID("input_manifest", model.ID), EntityID: model.ID.String(), EntityType: "inputManifest",
			Title: "AI input · " + manifest.JobType, Status: "frozen", Metadata: metadata, UpdatedAt: model.CreatedAt,
		})
		if manifest.BaseRevision != nil {
			edgeMetadata, _ := json.Marshal(map[string]any{
				"revisionId": manifest.BaseRevision.RevisionID, "contentHash": manifest.BaseRevision.ContentHash,
				"anchorId": manifest.BaseRevision.AnchorID,
			})
			edges = append(edges, DocumentGraphEdge{
				ID:       "manifest-base:" + model.ID.String(),
				SourceID: graphArtifactNodeID(manifest.BaseRevision.ArtifactID), TargetID: graphNodeID("input_manifest", model.ID),
				Relation: "proposal_base", Required: true, Metadata: edgeMetadata,
			})
		}
		for index, source := range manifest.Sources {
			edgeMetadata, _ := json.Marshal(map[string]any{
				"revisionId": source.Ref.RevisionID, "contentHash": source.Ref.ContentHash,
				"anchorId": source.Ref.AnchorID, "purpose": source.Purpose,
			})
			edges = append(edges, DocumentGraphEdge{
				ID:       fmt.Sprintf("manifest-source:%s:%04d", model.ID, index),
				SourceID: graphArtifactNodeID(source.Ref.ArtifactID), TargetID: graphNodeID("input_manifest", model.ID),
				Relation: "frozen_input", Required: true, Metadata: edgeMetadata,
			})
		}
	}
	if len(manifestIDs) == 0 {
		return nodes, edges, nil
	}
	var proposalModels []storage.OutputProposalModel
	if err := s.database.WithContext(ctx).Where("project_id = ? AND input_manifest_id IN ?", projectID, manifestIDs).
		Order("created_at DESC, id DESC").Limit(1000).Find(&proposalModels).Error; err != nil {
		return nil, nil, err
	}
	type decisionRow struct {
		ProposalID  uuid.UUID
		OperationID string
		Decision    string
		Reason      string
		DecidedBy   uuid.UUID
		DecidedAt   time.Time
	}
	var decisionRows []decisionRow
	if len(proposalModels) > 0 {
		proposalIDs := make([]uuid.UUID, 0, len(proposalModels))
		for _, proposal := range proposalModels {
			proposalIDs = append(proposalIDs, proposal.ID)
		}
		if err := s.database.WithContext(ctx).Table("proposal_operation_decisions").
			Where("proposal_id IN ?", proposalIDs).
			Order("proposal_id ASC, operation_id ASC").Limit(5000).Scan(&decisionRows).Error; err != nil {
			return nil, nil, err
		}
	}
	decisions := make(map[uuid.UUID][]map[string]any)
	for _, decision := range decisionRows {
		decisions[decision.ProposalID] = append(decisions[decision.ProposalID], map[string]any{
			"operationId": decision.OperationID, "decision": decision.Decision, "reason": decision.Reason,
			"decidedBy": decision.DecidedBy.String(), "decidedAt": decision.DecidedAt,
		})
	}
	for _, model := range proposalModels {
		manifest, exists := manifests[model.InputManifestID]
		if !exists || model.ArtifactID == nil || model.BaseRevisionID == nil || model.BaseContentHash == nil {
			return nil, nil, core.ErrConflict
		}
		updatedAt := model.CreatedAt
		if model.AppliedAt != nil {
			updatedAt = *model.AppliedAt
		}
		metadata, _ := json.Marshal(map[string]any{
			"jobType": model.Kind, "payloadHash": model.PayloadHash, "contentHash": model.ContentHash,
			"operationCount": model.OperationCount, "acceptedCount": model.AcceptedCount,
			"rejectedCount": model.RejectedCount, "pendingCount": model.OperationCount - model.AcceptedCount - model.RejectedCount,
			"operationDecisions": decisions[model.ID], "aiProvider": stringValue(model.AIProvider),
			"aiModel": stringValue(model.AIModel), "manifestHash": manifest.Hash,
		})
		nodes = append(nodes, DocumentGraphNode{
			ID: graphNodeID("output_proposal", model.ID), EntityID: model.ID.String(), EntityType: "outputProposal",
			Title: "AI output · " + model.Kind, Status: model.Status, Metadata: metadata, UpdatedAt: updatedAt,
		})
		edges = append(edges, DocumentGraphEdge{
			ID: "manifest-output:" + model.ID.String(), SourceID: graphNodeID("input_manifest", model.InputManifestID),
			TargetID: graphNodeID("output_proposal", model.ID), Relation: "generated_output", Required: true,
			Metadata: json.RawMessage(`{}`),
		})
		targetMetadata, _ := json.Marshal(map[string]any{
			"baseRevisionId": model.BaseRevisionID.String(), "baseContentHash": *model.BaseContentHash,
			"proposalStatus": model.Status,
		})
		edges = append(edges, DocumentGraphEdge{
			ID: "output-target:" + model.ID.String(), SourceID: graphNodeID("output_proposal", model.ID),
			TargetID: graphNodeID("artifact", *model.ArtifactID), Relation: "proposes_patch", Required: true,
			Metadata: targetMetadata,
		})
	}
	return nodes, edges, nil
}

func graphArtifactNodeID(artifactID string) string {
	parsed, err := uuid.Parse(strings.TrimSpace(artifactID))
	if err != nil {
		return "artifact:invalid:" + strings.TrimSpace(artifactID)
	}
	return graphNodeID("artifact", parsed)
}

func (s *Service) artifactGraph(ctx context.Context, projectID uuid.UUID) ([]DocumentGraphNode, []DocumentGraphEdge, error) {
	type artifactRow struct {
		ID                       uuid.UUID
		Kind                     string
		Title                    string
		Lifecycle                string
		LatestRevisionID         *uuid.UUID
		LatestApprovedRevisionID *uuid.UUID
		CreatedBy                uuid.UUID
		CreatedAt                time.Time
		UpdatedAt                time.Time
		RevisionID               *uuid.UUID
		RevisionHash             *string
		RevisionStatus           *string
		SyncStatus               *string
	}
	var rows []artifactRow
	if err := s.database.WithContext(ctx).Table("artifacts AS artifact").Select(`
		artifact.id, artifact.kind, artifact.title, artifact.lifecycle,
		artifact.latest_revision_id, artifact.latest_approved_revision_id, artifact.created_by, artifact.created_at, artifact.updated_at,
		revision.id AS revision_id, revision.content_hash AS revision_hash,
		revision.workflow_status AS revision_status, health.sync_status`).
		Joins("LEFT JOIN artifact_revisions AS revision ON revision.id = artifact.latest_revision_id").
		Joins("LEFT JOIN artifact_health AS health ON health.artifact_id = artifact.id").
		Where("artifact.project_id = ?", projectID).Order("artifact.updated_at DESC, artifact.id ASC").Limit(1000).
		Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	var bindingModels []storage.ArtifactMemberBindingModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectID).
		Order("artifact_id ASC, role ASC, user_id ASC").Find(&bindingModels).Error; err != nil {
		return nil, nil, err
	}
	bindings := make(map[uuid.UUID][]DocumentMemberBinding)
	for _, model := range bindingModels {
		bindings[model.ArtifactID] = append(bindings[model.ArtifactID], DocumentMemberBinding{
			UserID: model.UserID.String(), Role: wireDocumentMemberRole(model.Role), Reason: model.Reason,
			AssignedBy: model.AssignedBy.String(), AssignedAt: model.AssignedAt,
		})
	}
	nodes := make([]DocumentGraphNode, 0, len(rows))
	for _, row := range rows {
		if len(bindings[row.ID]) == 0 {
			bindings[row.ID] = []DocumentMemberBinding{{
				UserID: row.CreatedBy.String(), Role: DocumentOwner, Reason: "Default artifact owner",
				AssignedBy: row.CreatedBy.String(), AssignedAt: row.CreatedAt,
			}}
		}
		status := "draft"
		if row.Lifecycle == "archived" {
			status = "archived"
		} else if row.SyncStatus != nil && (*row.SyncStatus == "needs_sync" || *row.SyncStatus == "blocked") {
			status = *row.SyncStatus
		} else if row.RevisionStatus != nil {
			status = *row.RevisionStatus
		}
		var revision *core.VersionRef
		if row.RevisionID != nil && row.RevisionHash != nil {
			revision = &core.VersionRef{ArtifactID: row.ID.String(), RevisionID: row.RevisionID.String(), ContentHash: *row.RevisionHash}
		}
		metadata, _ := json.Marshal(map[string]any{
			"latestApprovedRevisionId": uuidString(row.LatestApprovedRevisionID),
			"bindingCount":             len(bindings[row.ID]),
		})
		nodes = append(nodes, DocumentGraphNode{
			ID: graphNodeID("artifact", row.ID), EntityID: row.ID.String(), EntityType: artifactGraphEntityType(row.Kind),
			ArtifactKind: row.Kind, Title: row.Title, Status: status, Revision: revision,
			Bindings: bindings[row.ID], Metadata: metadata, UpdatedAt: row.UpdatedAt,
		})
	}
	edges := make([]DocumentGraphEdge, 0)
	var dependencies []storage.ArtifactDependencyModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectID).Order("created_at ASC, id ASC").
		Limit(5000).Find(&dependencies).Error; err != nil {
		return nil, nil, err
	}
	for _, dependency := range dependencies {
		metadata, _ := json.Marshal(map[string]any{
			"sourceRevisionId": dependency.SourceRevisionID.String(), "targetRevisionId": uuidString(dependency.TargetRevisionID),
			"sourceContentHash": dependency.SourceContentHash,
		})
		edges = append(edges, DocumentGraphEdge{
			ID: "dependency:" + dependency.ID.String(), SourceID: graphNodeID("artifact", dependency.SourceArtifactID),
			TargetID: graphNodeID("artifact", dependency.TargetArtifactID), Relation: dependency.Relation,
			Required: dependency.Required, Metadata: metadata,
		})
	}
	var traces []storage.TraceLinkModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectID).Order("created_at ASC, id ASC").
		Limit(5000).Find(&traces).Error; err != nil {
		return nil, nil, err
	}
	for _, trace := range traces {
		metadata, _ := json.Marshal(map[string]any{
			"sourceRevisionId": trace.SourceRevisionID.String(), "targetRevisionId": uuidString(trace.TargetRevisionID),
			"sourceAnchorId": stringValue(trace.SourceAnchorID), "targetAnchorId": stringValue(trace.TargetAnchorID),
			"traceMetadata": trace.Metadata,
		})
		edges = append(edges, DocumentGraphEdge{
			ID: "trace:" + trace.ID.String(), SourceID: graphNodeID("artifact", trace.SourceArtifactID),
			TargetID: graphNodeID("artifact", trace.TargetArtifactID), Relation: trace.Relation,
			Metadata: metadata,
		})
	}
	return nodes, edges, nil
}

func (s *Service) implementationGraph(ctx context.Context, projectID uuid.UUID) ([]DocumentGraphNode, []DocumentGraphEdge, error) {
	var runs []storage.WorkflowRunModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectID).Order("created_at DESC, id DESC").Limit(500).
		Find(&runs).Error; err != nil {
		return nil, nil, err
	}
	nodes := make([]DocumentGraphNode, 0, len(runs))
	edges := make([]DocumentGraphEdge, 0)
	for _, run := range runs {
		metadata, _ := json.Marshal(map[string]any{"definitionVersionId": run.DefinitionVersionID.String()})
		nodes = append(nodes, DocumentGraphNode{
			ID: graphNodeID("workflow_run", run.ID), EntityID: run.ID.String(), EntityType: "workflowRun",
			Title: "Workflow run " + shortGraphID(run.ID), Status: run.Status, Metadata: metadata, UpdatedAt: run.UpdatedAt,
		})
	}
	manifests, err := s.activatedBuildManifestsForGraph(ctx, projectID, 1000)
	if err != nil {
		return nil, nil, err
	}
	for _, manifest := range manifests {
		metadata, _ := json.Marshal(map[string]any{
			"rootBuildManifestId": manifest.RootManifestID.String(), "manifestHash": manifest.ManifestHash,
			"manifestGroup": stringValue(manifest.ManifestGroupKey), "ordinal": intValue(manifest.RootOrdinal),
		})
		nodes = append(nodes, DocumentGraphNode{
			ID: graphNodeID("build_manifest", manifest.ID), EntityID: manifest.ID.String(), EntityType: "workbenchVersion",
			Title: "Workbench " + shortGraphID(manifest.ID), Status: manifest.Status, Metadata: metadata, UpdatedAt: manifest.CreatedAt,
		})
		if manifest.WorkflowRunID != nil {
			edges = append(edges, DocumentGraphEdge{
				ID: "run-build:" + manifest.ID.String(), SourceID: graphNodeID("workflow_run", *manifest.WorkflowRunID),
				TargetID: graphNodeID("build_manifest", manifest.ID), Relation: "compiled_into", Metadata: json.RawMessage(`{}`),
			})
		}
		if manifest.WorkspaceRevisionID != nil {
			var revision storage.ArtifactRevisionModel
			if err := s.database.WithContext(ctx).Select("artifact_id").Where("id = ?", *manifest.WorkspaceRevisionID).Take(&revision).Error; err == nil {
				edges = append(edges, DocumentGraphEdge{
					ID: "workspace-build:" + manifest.ID.String(), SourceID: graphNodeID("artifact", revision.ArtifactID),
					TargetID: graphNodeID("build_manifest", manifest.ID), Relation: "compiled_into", Metadata: json.RawMessage(`{}`),
				})
			}
		}
	}
	var proposals []storage.ImplementationProposalModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectID).Order("created_at DESC, id DESC").Limit(1000).
		Find(&proposals).Error; err != nil {
		return nil, nil, err
	}
	for _, proposal := range proposals {
		metadata, _ := json.Marshal(map[string]any{
			"buildManifestId": proposal.BuildManifestID.String(), "operationCount": proposal.OperationCount,
			"acceptedCount": proposal.AcceptedCount, "rejectedCount": proposal.RejectedCount,
		})
		nodes = append(nodes, DocumentGraphNode{
			ID: graphNodeID("implementation_proposal", proposal.ID), EntityID: proposal.ID.String(), EntityType: "implementation",
			Title: "Implementation " + shortGraphID(proposal.ID), Status: proposal.Status, Metadata: metadata, UpdatedAt: proposal.CreatedAt,
		})
		edges = append(edges, DocumentGraphEdge{
			ID: "build-implementation:" + proposal.ID.String(), SourceID: graphNodeID("build_manifest", proposal.BuildManifestID),
			TargetID: graphNodeID("implementation_proposal", proposal.ID), Relation: "implemented_by", Metadata: json.RawMessage(`{}`),
		})
	}
	return nodes, edges, nil
}

func (s *Service) activatedBuildManifestsForGraph(
	ctx context.Context,
	projectID uuid.UUID,
	limit int,
) ([]storage.ApplicationBuildManifestModel, error) {
	if limit <= 0 {
		return []storage.ApplicationBuildManifestModel{}, nil
	}
	const batchSize = 250
	result := make([]storage.ApplicationBuildManifestModel, 0, limit)
	var cursorCreatedAt time.Time
	var cursorID uuid.UUID
	for len(result) < limit {
		query := s.database.WithContext(ctx).
			Where("project_id = ?", projectID).
			Where(`workflow_run_id IS NULL OR manifest_group_key = 'legacy' OR EXISTS (
				SELECT 1
				FROM workflow_node_runs AS compiler
				WHERE CAST(compiler.id AS text) = application_build_manifests.manifest_group_key
				  AND compiler.run_id = application_build_manifests.workflow_run_id
				  AND compiler.node_type = ?
				  AND compiler.status = 'completed'
				  AND compiler.completed_at IS NOT NULL
			)`, domain.NodeManifestCompiler)
		if !cursorCreatedAt.IsZero() {
			query = query.Where("(created_at, id) < (?, ?)", cursorCreatedAt, cursorID)
		}
		var batch []storage.ApplicationBuildManifestModel
		if err := query.Order("created_at DESC, id DESC").Limit(batchSize).Find(&batch).Error; err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, manifest := range batch {
			if err := core.EnsureWorkflowManifestGroupActivated(ctx, s.database, manifest); err != nil {
				if errors.Is(err, core.ErrBlockingGate) {
					continue
				}
				return nil, err
			}
			result = append(result, manifest)
			if len(result) == limit {
				break
			}
		}
		last := batch[len(batch)-1]
		cursorCreatedAt, cursorID = last.CreatedAt, last.ID
		if len(batch) < batchSize {
			break
		}
	}
	return result, nil
}

func (s *Service) deploymentGraph(ctx context.Context, projectID uuid.UUID) ([]DocumentGraphNode, []DocumentGraphEdge, error) {
	type deploymentRow struct {
		ID              uuid.UUID
		Environment     string
		Status          string
		ActiveVersionID *uuid.UUID
		PublicURL       *string
		UpdatedAt       time.Time
	}
	var deployments []deploymentRow
	if err := s.database.WithContext(ctx).Table("deployments").Where("project_id = ?", projectID).
		Order("updated_at DESC, id DESC").Limit(500).Find(&deployments).Error; err != nil {
		return nil, nil, err
	}
	nodes := make([]DocumentGraphNode, 0, len(deployments))
	edges := make([]DocumentGraphEdge, 0)
	for _, deployment := range deployments {
		metadata, _ := json.Marshal(map[string]any{
			"environment": deployment.Environment, "activeVersionId": uuidString(deployment.ActiveVersionID),
			"previewUrl": stringValue(deployment.PublicURL),
		})
		nodes = append(nodes, DocumentGraphNode{
			ID: graphNodeID("deployment", deployment.ID), EntityID: deployment.ID.String(), EntityType: "deployment",
			Title: deployment.Environment + " deployment", Status: deployment.Status, Metadata: metadata, UpdatedAt: deployment.UpdatedAt,
		})
		if deployment.ActiveVersionID == nil {
			continue
		}
		type versionRow struct {
			WorkspaceArtifactID uuid.UUID
			BuildManifestID     *uuid.UUID
		}
		var version versionRow
		if err := s.database.WithContext(ctx).Table("deployment_versions").Where(
			"id = ? AND deployment_id = ?", *deployment.ActiveVersionID, deployment.ID,
		).Take(&version).Error; err != nil {
			return nil, nil, err
		}
		edges = append(edges, DocumentGraphEdge{
			ID: "workspace-deployment:" + deployment.ID.String(), SourceID: graphNodeID("artifact", version.WorkspaceArtifactID),
			TargetID: graphNodeID("deployment", deployment.ID), Relation: "deployed_as", Metadata: json.RawMessage(`{}`),
		})
		if version.BuildManifestID != nil {
			edges = append(edges, DocumentGraphEdge{
				ID: "build-deployment:" + deployment.ID.String(), SourceID: graphNodeID("build_manifest", *version.BuildManifestID),
				TargetID: graphNodeID("deployment", deployment.ID), Relation: "deployed_as", Metadata: json.RawMessage(`{}`),
			})
		}
	}
	return nodes, edges, nil
}

func artifactGraphEntityType(kind string) string {
	switch kind {
	case "blueprint", "product_requirements", "project_brief":
		return "feature"
	case "page_spec", "prototype", "prototype_flow":
		return "page"
	case "api_contract":
		return "api"
	case "data_contract":
		return "data"
	case "permission_contract":
		return "permission"
	case "ai_runtime_contract":
		return "ai_runtime"
	case "deployment_contract":
		return "deployment"
	case "verification_contract":
		return "verification"
	case "workspace":
		return "workspace"
	default:
		return "document"
	}
}

func graphNodeID(kind string, id uuid.UUID) string { return kind + ":" + id.String() }

func shortGraphID(id uuid.UUID) string { return id.String()[:8] }

func uuidString(value *uuid.UUID) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
