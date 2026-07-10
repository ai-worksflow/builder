package collaboration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type ProvenanceKind string

const (
	ProvenanceWorkspaceRevision      ProvenanceKind = "workspaceRevision"
	ProvenanceImplementationProposal ProvenanceKind = "implementationProposal"
	ProvenanceBuildManifest          ProvenanceKind = "buildManifest"
	ProvenanceDeployment             ProvenanceKind = "deployment"
)

type ProvenanceRef struct {
	Kind ProvenanceKind `json:"kind"`
	ID   string         `json:"id"`
}

type CreateSyncBackProposalInput struct {
	TargetRevision core.VersionRef `json:"targetRevision"`
	Provenance     ProvenanceRef   `json:"provenance"`
	Instruction    string          `json:"instruction"`
	Model          string          `json:"model,omitempty"`
}

type SyncBackProposal struct {
	Manifest        domain.InputManifest  `json:"inputManifest"`
	Proposal        domain.OutputProposal `json:"proposal"`
	Provenance      ProvenanceRef         `json:"provenance"`
	WorkspaceSource *core.VersionRef      `json:"workspaceSource,omitempty"`
	PreviewURL      string                `json:"previewUrl,omitempty"`
	Provider        string                `json:"provider"`
	Model           string                `json:"model"`
}

type resolvedProvenance struct {
	Reference  ProvenanceRef
	Workspace  *core.VersionRef
	PreviewURL string
	Snapshot   map[string]any
}

func (s *Service) CreateSyncBackProposal(
	ctx context.Context,
	projectID, actorID string,
	input CreateSyncBackProposalInput,
) (SyncBackProposal, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionEdit); err != nil {
		return SyncBackProposal{}, err
	}
	input.Instruction = strings.TrimSpace(input.Instruction)
	input.Provenance.ID = strings.TrimSpace(input.Provenance.ID)
	if input.Instruction == "" || len(input.Instruction) > 8000 || input.Provenance.ID == "" {
		return SyncBackProposal{}, fmt.Errorf("%w: document sync-back request", core.ErrInvalidInput)
	}
	targetArtifact, _, err := s.requireApprovedDocumentRevision(ctx, projectID, input.TargetRevision)
	if err != nil {
		return SyncBackProposal{}, err
	}
	if targetArtifact.LatestRevisionID == nil || *targetArtifact.LatestRevisionID != uuid.MustParse(input.TargetRevision.RevisionID) ||
		targetArtifact.LatestApprovedRevisionID == nil || *targetArtifact.LatestApprovedRevisionID != *targetArtifact.LatestRevisionID {
		return SyncBackProposal{}, core.ErrProposalStale
	}
	provenance, err := s.resolveProvenance(ctx, projectID, input.Provenance)
	if err != nil {
		return SyncBackProposal{}, err
	}
	constraints, err := json.Marshal(map[string]any{
		"instruction": input.Instruction,
		"syncBack": map[string]any{
			"targetRevision": input.TargetRevision,
			"provenance":     provenance.Snapshot,
			"previewUrl":     provenance.PreviewURL,
		},
	})
	if err != nil {
		return SyncBackProposal{}, err
	}
	if provenance.Workspace == nil {
		return SyncBackProposal{}, core.ErrConflict
	}
	manifest, err := s.proposals.CreateDocumentSyncBackManifest(ctx, projectID, actorID, core.CreateDocumentSyncBackManifestInput{
		BaseRevision: input.TargetRevision, WorkspaceRevision: *provenance.Workspace, Constraints: constraints,
	})
	if err != nil {
		return SyncBackProposal{}, err
	}
	generated, err := s.generator.GenerateArtifactProposal(ctx, manifest.ID, actorID, input.Model)
	if err != nil {
		return SyncBackProposal{}, err
	}
	projectUUID := uuid.MustParse(projectID)
	actorUUID := uuid.MustParse(actorID)
	if err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := collaborationAudit(transaction, projectUUID, actorUUID, "document.sync_back_proposed", "output_proposal", generated.Proposal.ID, map[string]any{
			"targetArtifactId": targetArtifact.ID.String(), "provenanceKind": provenance.Reference.Kind,
			"provenanceId": provenance.Reference.ID, "manifestId": manifest.ID,
		}); err != nil {
			return err
		}
		return collaborationOutbox(transaction, "output_proposal", generated.Proposal.ID, "document.sync_back_proposed", "worksflow.document.sync_back.proposed", map[string]any{
			"projectId": projectID, "artifactId": targetArtifact.ID.String(), "proposalId": generated.Proposal.ID,
		})
	}); err != nil {
		return SyncBackProposal{}, err
	}
	return SyncBackProposal{
		Manifest: manifest, Proposal: generated.Proposal, Provenance: provenance.Reference,
		WorkspaceSource: provenance.Workspace, PreviewURL: provenance.PreviewURL,
		Provider: generated.Provider, Model: generated.Model,
	}, nil
}

func (s *Service) resolveProvenance(ctx context.Context, projectID string, reference ProvenanceRef) (resolvedProvenance, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return resolvedProvenance{}, fmt.Errorf("%w: project id", core.ErrInvalidInput)
	}
	id, err := uuid.Parse(reference.ID)
	if err != nil {
		return resolvedProvenance{}, fmt.Errorf("%w: provenance id", core.ErrInvalidInput)
	}
	reference.ID = id.String()
	result := resolvedProvenance{Reference: reference, Snapshot: map[string]any{"kind": reference.Kind, "id": reference.ID}}
	switch reference.Kind {
	case ProvenanceWorkspaceRevision:
		workspace, err := s.currentApprovedWorkspaceReference(ctx, projectUUID, id)
		if err != nil {
			return resolvedProvenance{}, err
		}
		proposal, manifest, err := s.appliedImplementationForWorkspaceRevision(ctx, projectUUID, id)
		if err != nil {
			return resolvedProvenance{}, err
		}
		result.Workspace = &workspace
		result.Snapshot["workspaceRevision"] = workspace
		result.Snapshot["implementationProposalId"] = proposal.ID.String()
		result.Snapshot["implementationStatus"] = proposal.Status
		result.Snapshot["implementationAppliedAt"] = proposal.AppliedAt
		result.Snapshot["implementationContentHash"] = proposal.ContentHash
		result.Snapshot["implementationPayloadHash"] = proposal.PayloadHash
		result.Snapshot["buildManifestId"] = manifest.ID.String()
		result.Snapshot["buildManifestHash"] = manifest.ManifestHash
		result.Snapshot["buildManifestContentHash"] = manifest.ContentHash
	case ProvenanceImplementationProposal:
		var proposal storage.ImplementationProposalModel
		if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", id, projectUUID).Take(&proposal).Error; err != nil {
			return resolvedProvenance{}, mapCollaborationNotFound(err)
		}
		if (proposal.Status != "applied" && proposal.Status != "partially_applied") ||
			proposal.AppliedAt == nil || proposal.AppliedBy == nil {
			return resolvedProvenance{}, core.ErrProposalStale
		}
		var manifest storage.ApplicationBuildManifestModel
		if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", proposal.BuildManifestID, projectUUID).
			Take(&manifest).Error; err != nil {
			return resolvedProvenance{}, mapCollaborationNotFound(err)
		}
		if err := s.requireConsumedManifestLeaf(ctx, projectUUID, manifest); err != nil {
			return resolvedProvenance{}, err
		}
		workspace, err := s.workspaceForImplementationProposal(ctx, projectUUID, proposal, manifest)
		if err != nil {
			return resolvedProvenance{}, err
		}
		result.Workspace = workspace
		result.Snapshot["status"] = proposal.Status
		result.Snapshot["appliedAt"] = proposal.AppliedAt
		result.Snapshot["proposalContentHash"] = proposal.ContentHash
		result.Snapshot["proposalPayloadHash"] = proposal.PayloadHash
		result.Snapshot["buildManifestId"] = proposal.BuildManifestID.String()
		result.Snapshot["buildManifestHash"] = manifest.ManifestHash
		if payload, err := s.provenanceContent(ctx, proposal.ContentRef, proposal.ContentHash); err == nil {
			result.Snapshot["implementationSummary"] = summarizeProvenancePayload(payload)
		} else {
			return resolvedProvenance{}, err
		}
	case ProvenanceBuildManifest:
		var manifest storage.ApplicationBuildManifestModel
		if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", id, projectUUID).Take(&manifest).Error; err != nil {
			return resolvedProvenance{}, mapCollaborationNotFound(err)
		}
		if err := s.requireConsumedManifestLeaf(ctx, projectUUID, manifest); err != nil {
			return resolvedProvenance{}, err
		}
		proposal, workspace, err := s.appliedProposalForBuildManifest(ctx, projectUUID, manifest.ID)
		if err != nil {
			return resolvedProvenance{}, err
		}
		result.Workspace = &workspace
		result.Snapshot["manifestHash"] = manifest.ManifestHash
		result.Snapshot["manifestContentHash"] = manifest.ContentHash
		result.Snapshot["rootBuildManifestId"] = manifest.RootManifestID.String()
		result.Snapshot["implementationProposalId"] = proposal.ID.String()
		result.Snapshot["implementationStatus"] = proposal.Status
		result.Snapshot["implementationAppliedAt"] = proposal.AppliedAt
		if payload, err := s.provenanceContent(ctx, manifest.ContentRef, manifest.ContentHash); err == nil {
			result.Snapshot["buildSummary"] = summarizeProvenancePayload(payload)
		} else {
			return resolvedProvenance{}, err
		}
	case ProvenanceDeployment:
		return s.resolveDeploymentProvenance(ctx, projectUUID, reference, id)
	default:
		return resolvedProvenance{}, fmt.Errorf("%w: provenance kind", core.ErrInvalidInput)
	}
	if result.Workspace != nil {
		result.Snapshot["workspaceRevision"] = *result.Workspace
	}
	return result, nil
}

func (s *Service) appliedImplementationForWorkspaceRevision(
	ctx context.Context,
	projectID, revisionID uuid.UUID,
) (storage.ImplementationProposalModel, storage.ApplicationBuildManifestModel, error) {
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Table("artifact_revisions AS revision").
		Select("revision.*").Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Where(
			"revision.id = ? AND revision.workflow_status = 'approved' AND artifact.project_id = ? AND artifact.kind = 'workspace' AND artifact.lifecycle = 'active'",
			revisionID, projectID,
		).Take(&revision).Error; err != nil {
		return storage.ImplementationProposalModel{}, storage.ApplicationBuildManifestModel{}, mapCollaborationNotFound(err)
	}
	if revision.ImplementationProposalID == nil {
		return storage.ImplementationProposalModel{}, storage.ApplicationBuildManifestModel{}, core.ErrProposalStale
	}
	var proposal storage.ImplementationProposalModel
	if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", *revision.ImplementationProposalID, projectID).
		Take(&proposal).Error; err != nil {
		return storage.ImplementationProposalModel{}, storage.ApplicationBuildManifestModel{}, mapCollaborationNotFound(err)
	}
	if (proposal.Status != "applied" && proposal.Status != "partially_applied") ||
		proposal.AppliedAt == nil || proposal.AppliedBy == nil {
		return storage.ImplementationProposalModel{}, storage.ApplicationBuildManifestModel{}, core.ErrProposalStale
	}
	var manifest storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", proposal.BuildManifestID, projectID).
		Take(&manifest).Error; err != nil {
		return storage.ImplementationProposalModel{}, storage.ApplicationBuildManifestModel{}, mapCollaborationNotFound(err)
	}
	if err := s.requireConsumedManifestLeaf(ctx, projectID, manifest); err != nil {
		return storage.ImplementationProposalModel{}, storage.ApplicationBuildManifestModel{}, err
	}
	workspace, err := s.workspaceForImplementationProposal(ctx, projectID, proposal, manifest)
	if err != nil {
		return storage.ImplementationProposalModel{}, storage.ApplicationBuildManifestModel{}, err
	}
	if workspace.RevisionID != revisionID.String() {
		return storage.ImplementationProposalModel{}, storage.ApplicationBuildManifestModel{}, core.ErrConflict
	}
	return proposal, manifest, nil
}

func (s *Service) workspaceForImplementationProposal(
	ctx context.Context,
	projectID uuid.UUID,
	proposal storage.ImplementationProposalModel,
	manifest storage.ApplicationBuildManifestModel,
) (*core.VersionRef, error) {
	if manifest.ID != proposal.BuildManifestID || manifest.Status != "consumed" ||
		(proposal.Status != "applied" && proposal.Status != "partially_applied") ||
		proposal.AppliedAt == nil || proposal.AppliedBy == nil {
		return nil, core.ErrProposalStale
	}
	var appliedRevisions []storage.ArtifactRevisionModel
	err := s.database.WithContext(ctx).Table("artifact_revisions AS revision").
		Select("revision.*").Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Where("revision.implementation_proposal_id = ? AND revision.workflow_status = ? AND artifact.project_id = ? AND artifact.kind = ?",
			proposal.ID, "approved", projectID, "workspace").
		Order("revision.created_at DESC, revision.id DESC").Limit(2).Find(&appliedRevisions).Error
	if err != nil {
		return nil, err
	}
	if len(appliedRevisions) != 1 {
		return nil, core.ErrProposalStale
	}
	workspace, err := s.currentApprovedWorkspaceReference(ctx, projectID, appliedRevisions[0].ID)
	if err != nil {
		return nil, err
	}
	return &workspace, nil
}

func (s *Service) requireConsumedManifestLeaf(
	ctx context.Context,
	projectID uuid.UUID,
	manifest storage.ApplicationBuildManifestModel,
) error {
	if manifest.ProjectID != projectID || manifest.Status != "consumed" || manifest.InvalidatedAt != nil ||
		manifest.RootManifestID == uuid.Nil {
		return core.ErrProposalStale
	}
	var children int64
	if err := s.database.WithContext(ctx).Model(&storage.ApplicationBuildManifestModel{}).
		Where("project_id = ? AND derived_from_id = ?", projectID, manifest.ID).Count(&children).Error; err != nil {
		return err
	}
	if children != 0 {
		return core.ErrProposalStale
	}
	return nil
}

func (s *Service) appliedProposalForBuildManifest(
	ctx context.Context,
	projectID, manifestID uuid.UUID,
) (storage.ImplementationProposalModel, core.VersionRef, error) {
	var proposals []storage.ImplementationProposalModel
	if err := s.database.WithContext(ctx).Where(
		"project_id = ? AND build_manifest_id = ? AND status IN ? AND applied_at IS NOT NULL AND applied_by IS NOT NULL",
		projectID, manifestID, []string{"applied", "partially_applied"},
	).Order("applied_at DESC, id DESC").Limit(2).Find(&proposals).Error; err != nil {
		return storage.ImplementationProposalModel{}, core.VersionRef{}, err
	}
	if len(proposals) != 1 {
		return storage.ImplementationProposalModel{}, core.VersionRef{}, core.ErrProposalStale
	}
	var manifest storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", manifestID, projectID).Take(&manifest).Error; err != nil {
		return storage.ImplementationProposalModel{}, core.VersionRef{}, mapCollaborationNotFound(err)
	}
	workspace, err := s.workspaceForImplementationProposal(ctx, projectID, proposals[0], manifest)
	if err != nil {
		return storage.ImplementationProposalModel{}, core.VersionRef{}, err
	}
	return proposals[0], *workspace, nil
}

func (s *Service) approvedWorkspaceReference(ctx context.Context, projectID, revisionID uuid.UUID) (core.VersionRef, error) {
	type row struct {
		ArtifactID  uuid.UUID
		RevisionID  uuid.UUID
		ContentHash string
	}
	var value row
	if err := s.database.WithContext(ctx).Table("artifact_revisions AS revision").
		Select("artifact.id AS artifact_id, revision.id AS revision_id, revision.content_hash").
		Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Where("revision.id = ? AND revision.workflow_status = ? AND artifact.project_id = ? AND artifact.kind = ? AND artifact.lifecycle = ?",
			revisionID, "approved", projectID, "workspace", "active").Take(&value).Error; err != nil {
		return core.VersionRef{}, mapCollaborationNotFound(err)
	}
	return core.VersionRef{ArtifactID: value.ArtifactID.String(), RevisionID: value.RevisionID.String(), ContentHash: value.ContentHash}, nil
}

func (s *Service) currentApprovedWorkspaceReference(
	ctx context.Context,
	projectID, revisionID uuid.UUID,
) (core.VersionRef, error) {
	reference, err := s.approvedWorkspaceReference(ctx, projectID, revisionID)
	if err != nil {
		return core.VersionRef{}, err
	}
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", reference.ArtifactID, projectID).
		Take(&artifact).Error; err != nil {
		return core.VersionRef{}, mapCollaborationNotFound(err)
	}
	if artifact.LatestRevisionID == nil || artifact.LatestApprovedRevisionID == nil ||
		*artifact.LatestRevisionID != revisionID || *artifact.LatestApprovedRevisionID != revisionID {
		return core.VersionRef{}, core.ErrProposalStale
	}
	return reference, nil
}

func (s *Service) provenanceContent(ctx context.Context, contentRef, contentHash string) (json.RawMessage, error) {
	stored, err := s.contents.Get(ctx, contentRef, contentHash)
	if err != nil {
		return nil, err
	}
	return stored.Payload, nil
}

func summarizeProvenancePayload(payload json.RawMessage) any {
	var object map[string]any
	if json.Unmarshal(payload, &object) != nil {
		return map[string]any{"contentHashOnly": true}
	}
	result := make(map[string]any)
	for _, key := range []string{
		"id", "status", "manifestHash", "routes", "apis", "migrations", "tests", "previews",
		"traceLinks", "diagnostics", "assumptions", "unimplementedItems", "currentWorkspaceRevision",
	} {
		if value, exists := object[key]; exists {
			result[key] = value
		}
	}
	if operations, ok := object["operations"].([]any); ok {
		if len(operations) > 200 {
			operations = operations[:200]
		}
		result["operations"] = operations
	}
	return result
}

func (s *Service) resolveDeploymentProvenance(
	ctx context.Context,
	projectID uuid.UUID,
	reference ProvenanceRef,
	deploymentID uuid.UUID,
) (resolvedProvenance, error) {
	type deploymentRow struct {
		ID              uuid.UUID
		ProjectID       uuid.UUID
		Environment     string
		Status          string
		ActiveVersionID *uuid.UUID
		PublicURL       *string
	}
	var deployment deploymentRow
	if err := s.database.WithContext(ctx).Table("deployments").Where("id = ? AND project_id = ?", deploymentID, projectID).
		Take(&deployment).Error; err != nil {
		return resolvedProvenance{}, mapCollaborationNotFound(err)
	}
	if deployment.ActiveVersionID == nil || deployment.Status != "ready" {
		return resolvedProvenance{}, core.ErrProposalStale
	}
	type versionRow struct {
		ID                   uuid.UUID
		DeploymentID         uuid.UUID
		WorkspaceArtifactID  uuid.UUID
		WorkspaceRevisionID  uuid.UUID
		WorkspaceContentHash string
		BuildManifestID      *uuid.UUID
		PublicURL            *string
		Checksum             string
		Status               string
		Message              string
		CreatedAt            time.Time
	}
	var version versionRow
	if err := s.database.WithContext(ctx).Table("deployment_versions").Where(
		"id = ? AND deployment_id = ?", *deployment.ActiveVersionID, deployment.ID,
	).Take(&version).Error; err != nil {
		return resolvedProvenance{}, mapCollaborationNotFound(err)
	}
	if version.Status != "ready" {
		return resolvedProvenance{}, core.ErrProposalStale
	}
	workspace, err := s.currentApprovedWorkspaceReference(ctx, projectID, version.WorkspaceRevisionID)
	if err != nil {
		return resolvedProvenance{}, err
	}
	if workspace.ArtifactID != version.WorkspaceArtifactID.String() || workspace.ContentHash != version.WorkspaceContentHash {
		return resolvedProvenance{}, core.ErrConflict
	}
	previewURL := ""
	if version.PublicURL != nil {
		previewURL = *version.PublicURL
	} else if deployment.PublicURL != nil {
		previewURL = *deployment.PublicURL
	}
	snapshot := map[string]any{
		"kind": reference.Kind, "id": reference.ID, "deploymentVersionId": version.ID.String(),
		"environment": deployment.Environment, "status": deployment.Status, "checksum": version.Checksum,
		"message": version.Message, "workspaceRevision": workspace, "previewUrl": previewURL,
	}
	if version.BuildManifestID == nil {
		return resolvedProvenance{}, core.ErrProposalStale
	}
	var manifest storage.ApplicationBuildManifestModel
	if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", *version.BuildManifestID, projectID).
		Take(&manifest).Error; err != nil {
		return resolvedProvenance{}, mapCollaborationNotFound(err)
	}
	if err := s.requireConsumedManifestLeaf(ctx, projectID, manifest); err != nil {
		return resolvedProvenance{}, err
	}
	proposal, implementedWorkspace, err := s.appliedProposalForBuildManifest(ctx, projectID, manifest.ID)
	if err != nil {
		return resolvedProvenance{}, err
	}
	if implementedWorkspace != workspace {
		return resolvedProvenance{}, core.ErrConflict
	}
	snapshot["buildManifestId"] = version.BuildManifestID.String()
	snapshot["buildManifestHash"] = manifest.ManifestHash
	snapshot["implementationProposalId"] = proposal.ID.String()
	return resolvedProvenance{
		Reference: reference, Workspace: &workspace, PreviewURL: previewURL, Snapshot: snapshot,
	}, nil
}
