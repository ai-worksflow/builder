package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ImplementationDecision string

const (
	ImplementationPending  ImplementationDecision = "pending"
	ImplementationAccepted ImplementationDecision = "accepted"
	ImplementationRejected ImplementationDecision = "rejected"
	ImplementationApplied  ImplementationDecision = "applied"
)

type FileOperation struct {
	ID           string                 `json:"id"`
	Kind         string                 `json:"kind"`
	Path         string                 `json:"path"`
	FromPath     string                 `json:"fromPath,omitempty"`
	Content      *string                `json:"content,omitempty"`
	Language     string                 `json:"language,omitempty"`
	ExpectedHash string                 `json:"expectedHash,omitempty"`
	DependsOn    []string               `json:"dependsOn,omitempty"`
	Rationale    string                 `json:"rationale,omitempty"`
	TraceSource  []string               `json:"traceSource,omitempty"`
	Decision     ImplementationDecision `json:"decision"`
	DecidedBy    string                 `json:"decidedBy,omitempty"`
	Reason       string                 `json:"reason,omitempty"`
}

type ImplementationProposal struct {
	ID                    string              `json:"id"`
	ProjectID             string              `json:"projectId"`
	BuildManifestID       string              `json:"buildManifestId"`
	BaseWorkspaceRevision *VersionRef         `json:"baseWorkspaceRevision,omitempty"`
	Operations            []FileOperation     `json:"operations"`
	Routes                []json.RawMessage   `json:"routes"`
	APIs                  []json.RawMessage   `json:"apis"`
	Migrations            []json.RawMessage   `json:"migrations"`
	Tests                 []json.RawMessage   `json:"tests"`
	Previews              []json.RawMessage   `json:"previews"`
	TraceLinks            []json.RawMessage   `json:"traceLinks"`
	Diagnostics           []ValidationFinding `json:"diagnostics"`
	Assumptions           []string            `json:"assumptions"`
	UnimplementedItems    []string            `json:"unimplementedItems"`
	Status                string              `json:"status"`
	Version               uint64              `json:"version"`
	PayloadHash           string              `json:"payloadHash"`
	CreatedBy             string              `json:"createdBy"`
	CreatedAt             time.Time           `json:"createdAt"`
	AppliedAt             *time.Time          `json:"appliedAt,omitempty"`
}

type CreateImplementationProposalInput struct {
	BuildManifestID    string              `json:"buildManifestId"`
	Operations         []FileOperation     `json:"operations"`
	Routes             []json.RawMessage   `json:"routes,omitempty"`
	APIs               []json.RawMessage   `json:"apis,omitempty"`
	Migrations         []json.RawMessage   `json:"migrations,omitempty"`
	Tests              []json.RawMessage   `json:"tests,omitempty"`
	Previews           []json.RawMessage   `json:"previews,omitempty"`
	TraceLinks         []json.RawMessage   `json:"traceLinks,omitempty"`
	Diagnostics        []ValidationFinding `json:"diagnostics,omitempty"`
	Assumptions        []string            `json:"assumptions,omitempty"`
	UnimplementedItems []string            `json:"unimplementedItems,omitempty"`
}

type DecideImplementationInput struct {
	OperationID string                 `json:"operationId"`
	Decision    ImplementationDecision `json:"decision"`
	Reason      string                 `json:"reason,omitempty"`
	Version     uint64                 `json:"version"`
}

type ApplyImplementationInput struct {
	Version uint64 `json:"version"`
}

type ImplementationService struct {
	database  *gorm.DB
	contents  content.Store
	access    *AccessControl
	workbench *WorkbenchService
	now       func() time.Time
}

func NewImplementationService(database *gorm.DB, contents content.Store, access *AccessControl) (*ImplementationService, error) {
	workbench, err := NewWorkbenchService(database, contents, access)
	if err != nil {
		return nil, err
	}
	return &ImplementationService{database: database, contents: contents, access: access, workbench: workbench, now: time.Now}, nil
}

func (s *ImplementationService) Create(ctx context.Context, projectID, actorID string, input CreateImplementationProposalInput) (ImplementationProposal, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return ImplementationProposal{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	bundle, err := s.workbench.GetBundle(ctx, input.BuildManifestID, actorID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	if bundle.ProjectID != projectID {
		return ImplementationProposal{}, ErrNotFound
	}
	if err := validateFileOperations(input.Operations); err != nil {
		return ImplementationProposal{}, err
	}
	proposalID := uuid.New()
	now := s.now().UTC()
	proposal := ImplementationProposal{
		ID: proposalID.String(), ProjectID: projectID, BuildManifestID: input.BuildManifestID,
		BaseWorkspaceRevision: cloneVersionRef(bundle.CurrentWorkspaceRevision),
		Operations:            cloneFileOperations(input.Operations), Routes: cloneRawMessages(input.Routes),
		APIs: cloneRawMessages(input.APIs), Migrations: cloneRawMessages(input.Migrations),
		Tests: cloneRawMessages(input.Tests), Previews: cloneRawMessages(input.Previews),
		TraceLinks: cloneRawMessages(input.TraceLinks), Diagnostics: append([]ValidationFinding(nil), input.Diagnostics...),
		Assumptions:        append([]string(nil), input.Assumptions...),
		UnimplementedItems: append([]string(nil), input.UnimplementedItems...),
		Status:             "open", Version: 1, CreatedBy: actorID, CreatedAt: now,
	}
	payloadHash, err := implementationPayloadHash(proposal)
	if err != nil {
		return ImplementationProposal{}, err
	}
	proposal.PayloadHash = payloadHash
	payload, err := json.Marshal(proposal)
	if err != nil {
		return ImplementationProposal{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, projectID, "implementation_proposal", proposal.ID, 1, payload)
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	buildManifestUUID := uuid.MustParse(input.BuildManifestID)
	var baseRevisionID *uuid.UUID
	if proposal.BaseWorkspaceRevision != nil {
		parsed := uuid.MustParse(proposal.BaseWorkspaceRevision.RevisionID)
		baseRevisionID = &parsed
	}
	model := storage.ImplementationProposalModel{
		ID: proposalID, ProjectID: projectUUID, BuildManifestID: buildManifestUUID,
		BaseWorkspaceRevisionID: baseRevisionID, Status: proposal.Status, Version: proposal.Version,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		PayloadHash: proposal.PayloadHash, OperationCount: len(proposal.Operations),
		CreatedBy: actorUUID, CreatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "implementation.proposal_created", "implementation_proposal", proposal.ID, map[string]any{"buildManifestId": input.BuildManifestID}); err != nil {
			return err
		}
		return enqueue(transaction, "implementation_proposal", proposal.ID, "implementation.proposal_created", "worksflow.implementation.proposal.created", map[string]any{
			"projectId": projectID, "proposalId": proposal.ID, "buildManifestId": input.BuildManifestID,
		})
	})
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return ImplementationProposal{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return proposal, nil
}

func (s *ImplementationService) Get(ctx context.Context, proposalID, actorID string) (ImplementationProposal, error) {
	proposal, model, err := s.load(ctx, proposalID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionView); err != nil {
		return ImplementationProposal{}, err
	}
	return proposal, nil
}

func (s *ImplementationService) Decide(ctx context.Context, proposalID, actorID string, input DecideImplementationInput) (ImplementationProposal, error) {
	proposal, model, err := s.load(ctx, proposalID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionEdit); err != nil {
		return ImplementationProposal{}, err
	}
	if input.Version == 0 {
		input.Version = proposal.Version
	}
	if proposal.Version != input.Version || proposal.Status == "applied" || proposal.Status == "partially_applied" || proposal.Status == "stale" {
		return ImplementationProposal{}, ErrConflict
	}
	if input.Decision != ImplementationAccepted && input.Decision != ImplementationRejected {
		return ImplementationProposal{}, fmt.Errorf("%w: implementation decision", ErrInvalidInput)
	}
	operationIndex := -1
	for index := range proposal.Operations {
		if proposal.Operations[index].ID == input.OperationID {
			operationIndex = index
			break
		}
	}
	if operationIndex < 0 {
		return ImplementationProposal{}, ErrNotFound
	}
	if proposal.Operations[operationIndex].Decision != ImplementationPending {
		return ImplementationProposal{}, ErrConflict
	}
	if input.Decision == ImplementationRejected && strings.TrimSpace(input.Reason) == "" {
		return ImplementationProposal{}, fmt.Errorf("%w: rejection reason", ErrInvalidInput)
	}
	proposal.Operations[operationIndex].Decision = input.Decision
	proposal.Operations[operationIndex].DecidedBy = actorID
	proposal.Operations[operationIndex].Reason = strings.TrimSpace(input.Reason)
	proposal.Version++
	proposal.Status = implementationStatus(proposal.Operations)
	payload, err := json.Marshal(proposal)
	if err != nil {
		return ImplementationProposal{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, model.ProjectID.String(), "implementation_proposal", proposalID, 1, payload)
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	accepted, rejected := implementationDecisionCounts(proposal.Operations)
	actorUUID := uuid.MustParse(actorID)
	now := s.now().UTC()
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&storage.ImplementationProposalModel{}).
			Where("id = ? AND version = ?", model.ID, input.Version).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version,
				"content_ref": contentRef.ID, "content_hash": contentRef.ContentHash,
				"accepted_count": accepted, "rejected_count": rejected,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		decision := storage.ImplementationOperationDecisionModel{
			ProposalID: model.ID, OperationID: input.OperationID, Decision: string(input.Decision),
			Reason: strings.TrimSpace(input.Reason), DecidedBy: actorUUID, DecidedAt: now,
		}
		if err := transaction.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "proposal_id"}, {Name: "operation_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"decision", "reason", "decided_by", "decided_at"}),
		}).Create(&decision).Error; err != nil {
			return err
		}
		return enqueue(transaction, "implementation_proposal", proposalID, "implementation.operation_decided", "worksflow.implementation.operation.decided", map[string]any{
			"projectId": model.ProjectID.String(), "proposalId": proposalID, "operationId": input.OperationID,
		})
	})
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return ImplementationProposal{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return proposal, nil
}

func (s *ImplementationService) Apply(ctx context.Context, proposalID, actorID string, input ApplyImplementationInput) (ArtifactRevision, error) {
	proposal, proposalModel, err := s.load(ctx, proposalID)
	if err != nil {
		return ArtifactRevision{}, err
	}
	if _, err := s.access.Authorize(ctx, proposalModel.ProjectID.String(), actorID, ActionEdit); err != nil {
		return ArtifactRevision{}, err
	}
	if input.Version == 0 {
		input.Version = proposal.Version
	}
	if proposal.Version != input.Version || proposal.Status != "ready" {
		return ArtifactRevision{}, ErrConflict
	}
	bundle, err := s.workbench.GetBundle(ctx, proposal.BuildManifestID, actorID)
	if err != nil {
		return ArtifactRevision{}, err
	}
	if bundle.ProjectID != proposalModel.ProjectID.String() {
		return ArtifactRevision{}, ErrConflict
	}
	accepted, err := acceptedImplementationOperations(proposal.Operations)
	if err != nil {
		return ArtifactRevision{}, err
	}
	workspace, workspaceArtifact, baseRevision, err := s.loadWorkspace(ctx, proposalModel.ProjectID, proposal.BaseWorkspaceRevision)
	if err != nil {
		return ArtifactRevision{}, err
	}
	workspace, err = applyFileOperations(workspace, accepted)
	if err != nil {
		return ArtifactRevision{}, err
	}
	now := s.now().UTC()
	workspace["updatedAt"] = now.Format(time.RFC3339Nano)
	workspacePayload, err := json.Marshal(workspace)
	if err != nil {
		return ArtifactRevision{}, err
	}
	workspaceRevisionID := uuid.New()
	workspaceContentRef, err := s.contents.PutPending(ctx, proposalModel.ProjectID.String(), "workspace_revision", workspaceRevisionID.String(), 1, workspacePayload)
	if err != nil {
		return ArtifactRevision{}, err
	}
	for index := range proposal.Operations {
		if proposal.Operations[index].Decision == ImplementationAccepted {
			proposal.Operations[index].Decision = ImplementationApplied
		}
	}
	_, rejected := implementationDecisionCounts(proposal.Operations)
	if rejected > 0 {
		proposal.Status = "partially_applied"
	} else {
		proposal.Status = "applied"
	}
	proposal.Version++
	proposal.AppliedAt = &now
	proposalPayload, err := json.Marshal(proposal)
	if err != nil {
		_ = s.contents.Abort(context.Background(), workspaceContentRef.ID)
		return ArtifactRevision{}, err
	}
	proposalContentRef, err := s.contents.PutPending(ctx, proposalModel.ProjectID.String(), "implementation_proposal", proposalID, 1, proposalPayload)
	if err != nil {
		_ = s.contents.Abort(context.Background(), workspaceContentRef.ID)
		return ArtifactRevision{}, err
	}
	pending := []string{workspaceContentRef.ID, proposalContentRef.ID}
	defer func() {
		for _, contentID := range pending {
			_ = s.contents.Abort(context.Background(), contentID)
		}
	}()
	actorUUID := uuid.MustParse(actorID)
	var revision storage.ArtifactRevisionModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if workspaceArtifact.ID == uuid.Nil {
			workspaceArtifact = storage.ArtifactModel{
				ID: uuid.New(), ProjectID: proposalModel.ProjectID, Kind: "workspace", ArtifactKey: "WORKSPACE-MAIN",
				Title: "Application Workspace", Lifecycle: "active", Version: 1,
				CreatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&workspaceArtifact).Error; err != nil {
				return err
			}
		} else {
			var locked storage.ArtifactModel
			if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", workspaceArtifact.ID).Take(&locked).Error; err != nil {
				return err
			}
			if baseRevision.ID == uuid.Nil || locked.LatestApprovedRevisionID == nil || *locked.LatestApprovedRevisionID != baseRevision.ID {
				return ErrProposalStale
			}
			workspaceArtifact = locked
		}
		var latest uint64
		if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("artifact_id = ?", workspaceArtifact.ID).
			Select("COALESCE(MAX(revision_number), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		revisionID := workspaceRevisionID
		revision = storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: workspaceArtifact.ID, RevisionNumber: latest + 1,
			SchemaVersion: 1, ContentStore: "mongo", ContentRef: workspaceContentRef.ID,
			ContentHash: workspaceContentRef.ContentHash, ByteSize: workspaceContentRef.ByteSize,
			WorkflowStatus: "approved", ChangeSource: "ai_proposal",
			ChangeSummary:            "Apply implementation proposal " + proposalID,
			ImplementationProposalID: &proposalModel.ID, CreatedBy: actorUUID, CreatedAt: now, ApprovedAt: &now,
		}
		if baseRevision.ID != uuid.Nil {
			revision.ParentRevisionID = &baseRevision.ID
			if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", baseRevision.ID).
				Updates(map[string]any{"workflow_status": "superseded", "superseded_at": now}).Error; err != nil {
				return err
			}
		}
		if err := transaction.Create(&revision).Error; err != nil {
			return err
		}
		draftID := uuid.New()
		draft := storage.ArtifactDraftModel{
			ID: draftID, ArtifactID: workspaceArtifact.ID, BaseRevisionID: &revision.ID,
			Sequence: 1, ETag: draftETag(draftID, 1, revision.ContentHash), SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: revision.ContentRef, ContentHash: revision.ContentHash,
			ByteSize: revision.ByteSize, Status: "draft", CreatedBy: actorUUID, UpdatedBy: actorUUID,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := transaction.Create(&draft).Error; err != nil {
			return err
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", workspaceArtifact.ID).
			Updates(map[string]any{
				"latest_revision_id": revision.ID, "latest_approved_revision_id": revision.ID,
				"latest_draft_id": draft.ID, "version": gorm.Expr("version + 1"), "updated_at": now,
			}).Error; err != nil {
			return err
		}
		result := transaction.Model(&storage.ImplementationProposalModel{}).
			Where("id = ? AND version = ?", proposalModel.ID, input.Version).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version,
				"content_ref": proposalContentRef.ID, "content_hash": proposalContentRef.ContentHash,
				"applied_by": actorUUID, "applied_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		if err := transaction.Model(&storage.ApplicationBuildManifestModel{}).
			Where("id = ? AND status = 'frozen'", proposalModel.BuildManifestID).
			Update("status", "consumed").Error; err != nil {
			return err
		}
		for _, source := range buildManifestSources(bundle) {
			sourceArtifactID := uuid.MustParse(source.ArtifactID)
			sourceRevisionID := uuid.MustParse(source.RevisionID)
			link := storage.TraceLinkModel{
				ID: uuid.New(), ProjectID: proposalModel.ProjectID, SourceArtifactID: sourceArtifactID,
				SourceRevisionID: sourceRevisionID, TargetArtifactID: workspaceArtifact.ID,
				TargetRevisionID: &revision.ID, Relation: "implemented_by", Metadata: json.RawMessage(`{}`),
				CreatedBy: actorUUID, CreatedAt: now,
			}
			if err := transaction.Create(&link).Error; err != nil {
				return err
			}
		}
		applyMetadata := map[string]any{"proposalId": proposalID, "appliedBaseRevisionId": nullableUUIDString(baseRevision.ID)}
		if proposal.BaseWorkspaceRevision != nil {
			applyMetadata["proposalBaseRevisionId"] = proposal.BaseWorkspaceRevision.RevisionID
		}
		if err := insertAudit(transaction, proposalModel.ProjectID, actorUUID, "implementation.applied", "artifact_revision", revision.ID.String(), applyMetadata); err != nil {
			return err
		}
		return enqueue(transaction, "workspace", workspaceArtifact.ID.String(), "implementation.applied", "worksflow.implementation.applied", map[string]any{
			"projectId": proposalModel.ProjectID.String(), "proposalId": proposalID,
			"workspaceArtifactId": workspaceArtifact.ID.String(), "workspaceRevisionId": revision.ID.String(),
			"appliedBaseRevisionId": nullableUUIDString(baseRevision.ID),
		})
	})
	if err != nil {
		return ArtifactRevision{}, err
	}
	pending = nil
	var finalizeErrors []error
	for _, contentID := range []string{workspaceContentRef.ID, proposalContentRef.ID} {
		if err := s.contents.Finalize(ctx, contentID); err != nil {
			finalizeErrors = append(finalizeErrors, err)
		}
	}
	if err := errors.Join(finalizeErrors...); err != nil {
		return ArtifactRevision{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return revisionFromModel(revision, workspacePayload, nil), nil
}

func (s *ImplementationService) load(ctx context.Context, proposalID string) (ImplementationProposal, storage.ImplementationProposalModel, error) {
	id, err := uuid.Parse(proposalID)
	if err != nil {
		return ImplementationProposal{}, storage.ImplementationProposalModel{}, fmt.Errorf("%w: implementation proposal id", ErrInvalidInput)
	}
	var model storage.ImplementationProposalModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ImplementationProposal{}, model, ErrNotFound
		}
		return ImplementationProposal{}, model, err
	}
	stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
	if err != nil {
		return ImplementationProposal{}, model, err
	}
	var proposal ImplementationProposal
	if err := json.Unmarshal(stored.Payload, &proposal); err != nil {
		return ImplementationProposal{}, model, err
	}
	hash, err := implementationPayloadHash(proposal)
	if err != nil || hash != proposal.PayloadHash || hash != model.PayloadHash || proposal.Version != model.Version || proposal.Status != model.Status {
		return ImplementationProposal{}, model, ErrConflict
	}
	return proposal, model, nil
}

func (s *ImplementationService) loadWorkspace(ctx context.Context, projectID uuid.UUID, expected *VersionRef) (map[string]any, storage.ArtifactModel, storage.ArtifactRevisionModel, error) {
	var artifact storage.ArtifactModel
	err := s.database.WithContext(ctx).
		Where("project_id = ? AND kind = 'workspace' AND lifecycle = 'active'", projectID).
		Take(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if expected != nil {
			return nil, storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, ErrProposalStale
		}
		return emptyWorkspace(projectID), storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, nil
	}
	if err != nil {
		return nil, storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, err
	}
	if artifact.LatestApprovedRevisionID == nil {
		return nil, artifact, storage.ArtifactRevisionModel{}, ErrProposalStale
	}
	if expected != nil {
		expectedArtifactID, expectedRevisionID, err := (&TraceService{database: s.database}).validateRef(ctx, projectID, *expected)
		if err != nil || expectedArtifactID != artifact.ID {
			return nil, artifact, storage.ArtifactRevisionModel{}, ErrProposalStale
		}
		descends, err := s.workspaceRevisionDescendsFrom(ctx, artifact.ID, *artifact.LatestApprovedRevisionID, expectedRevisionID)
		if err != nil {
			return nil, artifact, storage.ArtifactRevisionModel{}, err
		}
		if !descends {
			return nil, artifact, storage.ArtifactRevisionModel{}, ErrProposalStale
		}
	}
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ? AND artifact_id = ?", *artifact.LatestApprovedRevisionID, artifact.ID).Take(&revision).Error; err != nil {
		return nil, artifact, revision, err
	}
	stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
	if err != nil {
		return nil, artifact, revision, err
	}
	var workspace map[string]any
	if err := json.Unmarshal(stored.Payload, &workspace); err != nil {
		return nil, artifact, revision, err
	}
	return workspace, artifact, revision, nil
}

func (s *ImplementationService) workspaceRevisionDescendsFrom(ctx context.Context, artifactID, latestID, ancestorID uuid.UUID) (bool, error) {
	var count int64
	err := s.database.WithContext(ctx).Raw(`
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
SELECT count(*) FROM lineage WHERE id = ?`, latestID, artifactID, artifactID, ancestorID).Scan(&count).Error
	return count == 1, err
}

func nullableUUIDString(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value.String()
}

func validateFileOperations(operations []FileOperation) error {
	if len(operations) == 0 || len(operations) > 10_000 {
		return fmt.Errorf("%w: implementation operations", ErrInvalidInput)
	}
	byID := map[string]FileOperation{}
	for index := range operations {
		operation := &operations[index]
		operation.ID = strings.TrimSpace(operation.ID)
		if operation.ID == "" || byID[operation.ID].ID != "" {
			return fmt.Errorf("%w: operation id", ErrInvalidInput)
		}
		if err := validateWorkspacePath(operation.Path); err != nil {
			return err
		}
		switch operation.Kind {
		case "file.upsert":
			if operation.Content == nil || len(*operation.Content) > 2<<20 {
				return fmt.Errorf("%w: file content at operation %d", ErrInvalidInput, index)
			}
		case "file.delete":
			if operation.Content != nil {
				return fmt.Errorf("%w: delete operation content", ErrInvalidInput)
			}
		case "file.rename":
			if err := validateWorkspacePath(operation.FromPath); err != nil || operation.FromPath == operation.Path {
				return fmt.Errorf("%w: rename paths", ErrInvalidInput)
			}
		default:
			return fmt.Errorf("%w: file operation kind", ErrInvalidInput)
		}
		operation.Decision = ImplementationPending
		operation.DecidedBy = ""
		operation.Reason = ""
		byID[operation.ID] = *operation
	}
	for _, operation := range operations {
		for _, dependency := range operation.DependsOn {
			if dependency == operation.ID || byID[dependency].ID == "" {
				return fmt.Errorf("%w: operation dependency", ErrInvalidInput)
			}
		}
	}
	if _, err := topologicalFileOperations(operations, false); err != nil {
		return err
	}
	return nil
}

func acceptedImplementationOperations(operations []FileOperation) ([]FileOperation, error) {
	for _, operation := range operations {
		if operation.Decision == ImplementationPending {
			return nil, ErrConflict
		}
		if operation.Decision == ImplementationAccepted {
			for _, dependency := range operation.DependsOn {
				dependencyOperation := findFileOperation(operations, dependency)
				if dependencyOperation == nil || dependencyOperation.Decision != ImplementationAccepted {
					return nil, fmt.Errorf("%w: accepted operation dependency", ErrBlockingGate)
				}
			}
		}
	}
	result, err := topologicalFileOperations(operations, true)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: no operations were accepted", ErrBlockingGate)
	}
	return result, nil
}

func topologicalFileOperations(operations []FileOperation, acceptedOnly bool) ([]FileOperation, error) {
	selected := map[string]FileOperation{}
	for _, operation := range operations {
		if !acceptedOnly || operation.Decision == ImplementationAccepted {
			selected[operation.ID] = operation
		}
	}
	indegree := map[string]int{}
	dependents := map[string][]string{}
	for id := range selected {
		indegree[id] = 0
	}
	for id, operation := range selected {
		for _, dependency := range operation.DependsOn {
			if _, included := selected[dependency]; !included {
				if acceptedOnly {
					continue
				}
				return nil, fmt.Errorf("%w: operation dependency", ErrInvalidInput)
			}
			indegree[id]++
			dependents[dependency] = append(dependents[dependency], id)
		}
	}
	queue := []string{}
	for id, count := range indegree {
		if count == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	result := make([]FileOperation, 0, len(selected))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		result = append(result, selected[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
				sort.Strings(queue)
			}
		}
	}
	if len(result) != len(selected) {
		return nil, fmt.Errorf("%w: operation dependency cycle", ErrInvalidInput)
	}
	return result, nil
}

func applyFileOperations(workspace map[string]any, operations []FileOperation) (map[string]any, error) {
	files := map[string]map[string]any{}
	for _, file := range objectSlice(workspace["files"]) {
		filePath := firstString(file, "path")
		if filePath != "" {
			files[filePath] = file
		}
	}
	for _, operation := range operations {
		switch operation.Kind {
		case "file.upsert":
			existing := files[operation.Path]
			if existing != nil {
				if operation.ExpectedHash == "" || operation.ExpectedHash != hashText(firstString(existing, "content")) {
					return nil, fmt.Errorf("%w: file %s changed", ErrProposalStale, operation.Path)
				}
			} else if operation.ExpectedHash != "" {
				return nil, fmt.Errorf("%w: file %s no longer exists", ErrProposalStale, operation.Path)
			}
			revision := 1
			if value, ok := existing["revision"].(float64); ok {
				revision = int(value) + 1
			}
			files[operation.Path] = map[string]any{
				"path": operation.Path, "content": dereferenceString(operation.Content),
				"language": operation.Language, "revision": revision, "dirty": false,
			}
		case "file.delete":
			existing := files[operation.Path]
			if existing == nil || operation.ExpectedHash == "" || operation.ExpectedHash != hashText(firstString(existing, "content")) {
				return nil, fmt.Errorf("%w: file %s changed", ErrProposalStale, operation.Path)
			}
			delete(files, operation.Path)
		case "file.rename":
			existing := files[operation.FromPath]
			if existing == nil || files[operation.Path] != nil || operation.ExpectedHash == "" || operation.ExpectedHash != hashText(firstString(existing, "content")) {
				return nil, fmt.Errorf("%w: rename source %s changed", ErrProposalStale, operation.FromPath)
			}
			delete(files, operation.FromPath)
			existing["path"] = operation.Path
			files[operation.Path] = existing
		}
	}
	paths := make([]string, 0, len(files))
	for filePath := range files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	fileList := make([]map[string]any, 0, len(paths))
	for _, filePath := range paths {
		fileList = append(fileList, files[filePath])
	}
	workspace["files"] = fileList
	if revision, ok := workspace["revision"].(float64); ok {
		workspace["revision"] = int(revision) + 1
	} else {
		workspace["revision"] = 1
	}
	return workspace, nil
}

func validateWorkspacePath(value string) error {
	if value == "" || len(value) > 512 || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return fmt.Errorf("%w: workspace path", ErrInvalidInput)
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%w: workspace path", ErrInvalidInput)
	}
	first := strings.Split(cleaned, "/")[0]
	if first == ".git" || first == ".ssh" || cleaned == ".env" || strings.HasPrefix(cleaned, ".env.") {
		return fmt.Errorf("%w: protected workspace path", ErrForbidden)
	}
	return nil
}

func implementationPayloadHash(proposal ImplementationProposal) (string, error) {
	type immutableOperation struct {
		ID           string   `json:"id"`
		Kind         string   `json:"kind"`
		Path         string   `json:"path"`
		FromPath     string   `json:"fromPath,omitempty"`
		Content      *string  `json:"content,omitempty"`
		Language     string   `json:"language,omitempty"`
		ExpectedHash string   `json:"expectedHash,omitempty"`
		DependsOn    []string `json:"dependsOn,omitempty"`
		Rationale    string   `json:"rationale,omitempty"`
		TraceSource  []string `json:"traceSource,omitempty"`
	}
	operations := make([]immutableOperation, len(proposal.Operations))
	for index, operation := range proposal.Operations {
		operations[index] = immutableOperation{
			operation.ID, operation.Kind, operation.Path, operation.FromPath, operation.Content,
			operation.Language, operation.ExpectedHash, operation.DependsOn, operation.Rationale, operation.TraceSource,
		}
	}
	payload := struct {
		ID                    string               `json:"id"`
		ProjectID             string               `json:"projectId"`
		BuildManifestID       string               `json:"buildManifestId"`
		BaseWorkspaceRevision *VersionRef          `json:"baseWorkspaceRevision,omitempty"`
		Operations            []immutableOperation `json:"operations"`
		Routes                []json.RawMessage    `json:"routes"`
		APIs                  []json.RawMessage    `json:"apis"`
		Migrations            []json.RawMessage    `json:"migrations"`
		Tests                 []json.RawMessage    `json:"tests"`
		Previews              []json.RawMessage    `json:"previews"`
		TraceLinks            []json.RawMessage    `json:"traceLinks"`
		Diagnostics           []ValidationFinding  `json:"diagnostics"`
		Assumptions           []string             `json:"assumptions"`
		UnimplementedItems    []string             `json:"unimplementedItems"`
		CreatedBy             string               `json:"createdBy"`
		CreatedAt             time.Time            `json:"createdAt"`
	}{
		proposal.ID, proposal.ProjectID, proposal.BuildManifestID, proposal.BaseWorkspaceRevision,
		operations, proposal.Routes, proposal.APIs, proposal.Migrations, proposal.Tests,
		proposal.Previews, proposal.TraceLinks, proposal.Diagnostics, proposal.Assumptions,
		proposal.UnimplementedItems, proposal.CreatedBy, proposal.CreatedAt,
	}
	return domain.CanonicalHash(payload)
}

func implementationStatus(operations []FileOperation) string {
	pending, accepted, rejected := 0, 0, 0
	for _, operation := range operations {
		switch operation.Decision {
		case ImplementationPending:
			pending++
		case ImplementationAccepted:
			accepted++
		case ImplementationRejected:
			rejected++
		}
	}
	switch {
	case pending > 0 && (accepted > 0 || rejected > 0):
		return "reviewing"
	case pending > 0:
		return "open"
	case accepted > 0:
		return "ready"
	default:
		return "rejected"
	}
}

func implementationDecisionCounts(operations []FileOperation) (int, int) {
	accepted, rejected := 0, 0
	for _, operation := range operations {
		if operation.Decision == ImplementationAccepted || operation.Decision == ImplementationApplied {
			accepted++
		} else if operation.Decision == ImplementationRejected {
			rejected++
		}
	}
	return accepted, rejected
}

func emptyWorkspace(projectID uuid.UUID) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return map[string]any{
		"schemaVersion": 1, "id": "workspace-" + projectID.String(), "name": "Application Workspace",
		"revision": 0, "createdAt": now, "updatedAt": now, "files": []any{},
		"checkpoints": []any{}, "branches": []any{}, "activeBranchId": "main", "diagnostics": []any{},
	}
}

func buildManifestSources(bundle WorkbenchBundle) []VersionRef {
	values := []VersionRef{bundle.BlueprintRevision, bundle.PageSpecRevision, bundle.PrototypeRevision}
	for _, collection := range [][]VersionRef{bundle.RequirementRevisions, bundle.ContractRevisions, bundle.DesignSystemRevisions} {
		for _, reference := range collection {
			values = appendUniqueRef(values, reference)
		}
	}
	return values
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func findFileOperation(values []FileOperation, id string) *FileOperation {
	for index := range values {
		if values[index].ID == id {
			return &values[index]
		}
	}
	return nil
}

func cloneFileOperations(values []FileOperation) []FileOperation {
	result := make([]FileOperation, len(values))
	for index, value := range values {
		value.DependsOn = append([]string(nil), value.DependsOn...)
		value.TraceSource = append([]string(nil), value.TraceSource...)
		if value.Content != nil {
			content := *value.Content
			value.Content = &content
		}
		value.Decision = ImplementationPending
		value.DecidedBy = ""
		value.Reason = ""
		result[index] = value
	}
	return result
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(values))
	for index, value := range values {
		result[index] = cloneJSON(value)
	}
	return result
}

func cloneVersionRef(value *VersionRef) *VersionRef {
	if value == nil {
		return nil
	}
	clone := *value
	if value.AnchorID != nil {
		anchor := *value.AnchorID
		clone.AnchorID = &anchor
	}
	return &clone
}
