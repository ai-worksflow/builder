package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var artifactKeyCharacters = regexp.MustCompile(`[^A-Z0-9]+`)

var validArtifactKinds = map[string]struct{}{
	"project_brief": {}, "product_requirements": {}, "decision_record": {},
	"glossary_policy": {}, "reference_source": {}, "change_request": {},
	"requirement_baseline": {}, "blueprint": {}, "page_spec": {}, "prototype": {},
	"prototype_flow": {}, "fixture_bundle": {}, "design_system": {}, "token_set": {},
	"component_registry": {}, "api_contract": {}, "data_contract": {},
	"permission_contract": {}, "workspace": {}, "test_report": {}, "quality_report": {},
}

type Artifact struct {
	ID                       string    `json:"id"`
	ProjectID                string    `json:"projectId"`
	Kind                     string    `json:"kind"`
	ArtifactKey              string    `json:"artifactKey"`
	Title                    string    `json:"title"`
	Lifecycle                string    `json:"lifecycle"`
	Status                   string    `json:"status"`
	SyncStatus               string    `json:"syncStatus"`
	DeliveryStatus           string    `json:"deliveryStatus"`
	LatestDraftID            *string   `json:"activeDraftId,omitempty"`
	LatestRevisionID         *string   `json:"latestRevisionId,omitempty"`
	LatestApprovedRevisionID *string   `json:"approvedRevisionId,omitempty"`
	Version                  uint64    `json:"version"`
	ETag                     string    `json:"etag"`
	CreatedBy                string    `json:"createdBy"`
	CreatedAt                time.Time `json:"createdAt"`
	UpdatedAt                time.Time `json:"updatedAt"`
}

type ArtifactDraft struct {
	ID             string           `json:"id"`
	ArtifactID     string           `json:"artifactId"`
	BaseRevisionID *string          `json:"baseRevisionId,omitempty"`
	Sequence       uint64           `json:"revision"`
	SchemaVersion  int              `json:"schemaVersion"`
	Content        json.RawMessage  `json:"content"`
	ContentHash    string           `json:"contentHash"`
	SourceVersions []ArtifactSource `json:"sourceVersions"`
	Status         string           `json:"status"`
	CreatedBy      string           `json:"createdBy"`
	UpdatedBy      string           `json:"updatedBy"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
	ETag           string           `json:"etag"`
}

type ArtifactRevision struct {
	ID               string          `json:"id"`
	ArtifactID       string          `json:"artifactId"`
	RevisionNumber   uint64          `json:"revisionNumber"`
	ParentRevisionID *string         `json:"basedOnRevisionId,omitempty"`
	SchemaVersion    int             `json:"schemaVersion"`
	Content          json.RawMessage `json:"content"`
	ContentHash      string          `json:"contentHash"`
	WorkflowStatus   string          `json:"status"`
	ChangeSource     string          `json:"changeSource"`
	ChangeSummary    string          `json:"changeSummary"`
	SourceManifestID *string         `json:"sourceManifestId,omitempty"`
	ProposalID       *string         `json:"proposalId,omitempty"`
	CreatedBy        string          `json:"createdBy"`
	CreatedAt        time.Time       `json:"createdAt"`
	ApprovedAt       *time.Time      `json:"approvedAt,omitempty"`
}

type VersionedArtifact struct {
	Artifact         Artifact          `json:"artifact"`
	Draft            *ArtifactDraft    `json:"draft,omitempty"`
	LatestRevision   *ArtifactRevision `json:"latestRevision,omitempty"`
	ApprovedRevision *ArtifactRevision `json:"approvedRevision,omitempty"`
}

type CreateArtifactInput struct {
	Kind           string                `json:"kind"`
	ArtifactKey    string                `json:"artifactKey,omitempty"`
	Title          string                `json:"title"`
	SchemaVersion  int                   `json:"schemaVersion,omitempty"`
	Content        json.RawMessage       `json:"content,omitempty"`
	SourceVersions []ArtifactSourceInput `json:"sourceVersions,omitempty"`
}

type UpdateDraftInput struct {
	SchemaVersion  int                   `json:"schemaVersion,omitempty"`
	Content        json.RawMessage       `json:"content"`
	SourceVersions []ArtifactSourceInput `json:"sourceVersions,omitempty"`
}

type ArtifactSourceInput struct {
	Ref      VersionRef `json:"version"`
	Purpose  string     `json:"purpose"`
	Required bool       `json:"required"`
}

type ArtifactSource struct {
	VersionRef
	Purpose  string `json:"purpose"`
	Required bool   `json:"required"`
}

type CreateRevisionInput struct {
	ChangeSummary string `json:"changeSummary"`
	ChangeSource  string `json:"changeSource,omitempty"`
}

type ArtifactService struct {
	database *gorm.DB
	contents content.Store
	access   *AccessControl
	now      func() time.Time
}

func NewArtifactService(database *gorm.DB, contents content.Store, access *AccessControl) (*ArtifactService, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("artifact database, content store and access control are required")
	}
	return &ArtifactService{database: database, contents: contents, access: access, now: time.Now}, nil
}

func (s *ArtifactService) Create(ctx context.Context, projectID, actorID string, input CreateArtifactInput) (VersionedArtifact, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return VersionedArtifact{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return VersionedArtifact{}, err
	}
	input.Kind = strings.TrimSpace(input.Kind)
	input.Title = strings.TrimSpace(input.Title)
	if _, valid := validArtifactKinds[input.Kind]; !valid || input.Title == "" || len(input.Title) > 240 {
		return VersionedArtifact{}, fmt.Errorf("%w: artifact kind or title", ErrInvalidInput)
	}
	if input.SchemaVersion <= 0 {
		input.SchemaVersion = 1
	}
	if len(input.Content) == 0 {
		input.Content = json.RawMessage(`{"schemaVersion":1}`)
	}
	artifactID := uuid.New()
	draftID := uuid.New()
	artifactKey := normalizeArtifactKey(input.ArtifactKey, input.Kind, artifactID)
	contentRef, err := s.contents.PutPending(ctx, projectID, "artifact_draft", draftID.String(), input.SchemaVersion, input.Content)
	if err != nil {
		return VersionedArtifact{}, fmt.Errorf("store artifact draft: %w", err)
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	now := s.now().UTC()
	artifactModel := storage.ArtifactModel{
		ID: artifactID, ProjectID: projectUUID, Kind: input.Kind, ArtifactKey: artifactKey,
		Title: input.Title, Lifecycle: "active", Version: 1, CreatedBy: actorUUID,
		CreatedAt: now, UpdatedAt: now,
	}
	draftModel := storage.ArtifactDraftModel{
		ID: draftID, ArtifactID: artifactID, Sequence: 1,
		ETag: draftETag(draftID, 1, contentRef.ContentHash), SchemaVersion: input.SchemaVersion,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		ByteSize: contentRef.ByteSize, Status: "draft", CreatedBy: actorUUID, UpdatedBy: actorUUID,
		CreatedAt: now, UpdatedAt: now,
	}
	sourceModels, err := s.validateSourceModels(ctx, projectUUID, draftID, actorUUID, input.SourceVersions)
	if err != nil {
		return VersionedArtifact{}, err
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&artifactModel).Error; err != nil {
			return err
		}
		if err := transaction.Create(&draftModel).Error; err != nil {
			return err
		}
		if len(sourceModels) > 0 {
			if err := transaction.Create(&sourceModels).Error; err != nil {
				return err
			}
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
			Update("latest_draft_id", draftID).Error; err != nil {
			return err
		}
		artifactModel.LatestDraftID = &draftID
		if err := transaction.Create(&storage.ArtifactHealthModel{
			ArtifactID: artifactID, SyncStatus: "current", DeliveryStatus: "incomplete",
			Report: json.RawMessage(`{}`), ComputedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "artifact.created", "artifact", artifactID.String(), map[string]any{"kind": input.Kind}); err != nil {
			return err
		}
		return enqueue(transaction, "artifact", artifactID.String(), "artifact.created", "worksflow.artifact.created", map[string]any{
			"projectId": projectID, "artifactId": artifactID.String(), "kind": input.Kind,
		})
	})
	if err != nil {
		return VersionedArtifact{}, fmt.Errorf("create artifact: %w", err)
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return VersionedArtifact{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	artifact := artifactFromModels(artifactModel, nil)
	draft := draftFromModel(draftModel, cloneJSON(input.Content), sourcesFromModels(sourceModels))
	return VersionedArtifact{Artifact: artifact, Draft: &draft}, nil
}

func (s *ArtifactService) List(ctx context.Context, projectID, actorID, kind, status string) ([]Artifact, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	type artifactRow struct {
		storage.ArtifactModel
		SyncStatus     *string `gorm:"column:sync_status"`
		DeliveryStatus *string `gorm:"column:delivery_status"`
		RevisionStatus *string `gorm:"column:revision_status"`
	}
	query := s.database.WithContext(ctx).Table("artifacts").
		Select("artifacts.*, artifact_health.sync_status, artifact_health.delivery_status, artifact_revisions.workflow_status AS revision_status").
		Joins("LEFT JOIN artifact_health ON artifact_health.artifact_id = artifacts.id").
		Joins("LEFT JOIN artifact_revisions ON artifact_revisions.id = artifacts.latest_revision_id").
		Where("artifacts.project_id = ?", projectUUID)
	if kind = strings.TrimSpace(kind); kind != "" {
		if _, valid := validArtifactKinds[kind]; !valid {
			return nil, fmt.Errorf("%w: artifact kind", ErrInvalidInput)
		}
		query = query.Where("artifacts.kind = ?", kind)
	}
	var rows []artifactRow
	if err := query.Order("artifacts.updated_at DESC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	result := make([]Artifact, 0, len(rows))
	for _, row := range rows {
		artifact := artifactFromModels(row.ArtifactModel, &artifactStatusFields{row.SyncStatus, row.DeliveryStatus, row.RevisionStatus})
		if status == "" || artifact.Status == status {
			result = append(result, artifact)
		}
	}
	return result, nil
}

func (s *ArtifactService) Get(ctx context.Context, artifactID, actorID string, includeContent bool) (VersionedArtifact, error) {
	artifactUUID, projectID, err := s.authorizeArtifact(ctx, artifactID, actorID, ActionView)
	if err != nil {
		return VersionedArtifact{}, err
	}
	_ = projectID
	var artifactModel storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where("id = ?", artifactUUID).Take(&artifactModel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return VersionedArtifact{}, ErrNotFound
		}
		return VersionedArtifact{}, err
	}
	var health storage.ArtifactHealthModel
	_ = s.database.WithContext(ctx).Where("artifact_id = ?", artifactUUID).Take(&health).Error
	fields := &artifactStatusFields{SyncStatus: &health.SyncStatus, DeliveryStatus: &health.DeliveryStatus}
	result := VersionedArtifact{Artifact: artifactFromModels(artifactModel, fields)}
	if artifactModel.LatestDraftID != nil {
		draft, err := s.loadDraft(ctx, *artifactModel.LatestDraftID, includeContent)
		if err != nil {
			return VersionedArtifact{}, err
		}
		result.Draft = &draft
	}
	if artifactModel.LatestRevisionID != nil {
		revision, err := s.loadRevision(ctx, *artifactModel.LatestRevisionID, includeContent)
		if err != nil {
			return VersionedArtifact{}, err
		}
		fields.RevisionStatus = &revision.WorkflowStatus
		result.Artifact = artifactFromModels(artifactModel, fields)
		result.LatestRevision = &revision
	}
	if artifactModel.LatestApprovedRevisionID != nil {
		if result.LatestRevision != nil && result.LatestRevision.ID == artifactModel.LatestApprovedRevisionID.String() {
			result.ApprovedRevision = result.LatestRevision
		} else {
			revision, err := s.loadRevision(ctx, *artifactModel.LatestApprovedRevisionID, includeContent)
			if err != nil {
				return VersionedArtifact{}, err
			}
			result.ApprovedRevision = &revision
		}
	}
	return result, nil
}

func (s *ArtifactService) UpdateDraft(ctx context.Context, draftID, actorID, expectedETag string, input UpdateDraftInput) (ArtifactDraft, error) {
	draftUUID, err := uuid.Parse(draftID)
	if err != nil {
		return ArtifactDraft{}, fmt.Errorf("%w: draft id", ErrInvalidInput)
	}
	var current storage.ArtifactDraftModel
	if err := s.database.WithContext(ctx).Where("id = ?", draftUUID).Take(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ArtifactDraft{}, ErrNotFound
		}
		return ArtifactDraft{}, err
	}
	_, projectUUID, err := s.authorizeArtifact(ctx, current.ArtifactID.String(), actorID, ActionEdit)
	if err != nil {
		return ArtifactDraft{}, err
	}
	if expectedETag == "" || expectedETag != current.ETag {
		return ArtifactDraft{}, ErrConflict
	}
	if current.Status != "draft" {
		return ArtifactDraft{}, ErrConflict
	}
	if input.SchemaVersion <= 0 {
		input.SchemaVersion = current.SchemaVersion
	}
	if len(input.Content) == 0 {
		return ArtifactDraft{}, fmt.Errorf("%w: draft content", ErrInvalidInput)
	}
	contentRef, err := s.contents.PutPending(ctx, projectUUID.String(), "artifact_draft", draftID, input.SchemaVersion, input.Content)
	if err != nil {
		return ArtifactDraft{}, fmt.Errorf("store draft content: %w", err)
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return ArtifactDraft{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	now := s.now().UTC()
	nextSequence := current.Sequence + 1
	nextETag := draftETag(draftUUID, nextSequence, contentRef.ContentHash)
	var replacementSources []storage.ArtifactDraftSourceModel
	if input.SourceVersions != nil {
		replacementSources, err = s.validateSourceModels(ctx, projectUUID, draftUUID, actorUUID, input.SourceVersions)
		if err != nil {
			return ArtifactDraft{}, err
		}
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&storage.ArtifactDraftModel{}).
			Where("id = ? AND etag = ? AND status = 'draft'", draftUUID, expectedETag).
			Updates(map[string]any{
				"sequence": nextSequence, "etag": nextETag, "schema_version": input.SchemaVersion,
				"content_ref": contentRef.ID, "content_hash": contentRef.ContentHash,
				"byte_size": contentRef.ByteSize, "updated_by": actorUUID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		if input.SourceVersions != nil {
			if err := transaction.Where("draft_id = ?", draftUUID).Delete(&storage.ArtifactDraftSourceModel{}).Error; err != nil {
				return err
			}
			if len(replacementSources) > 0 {
				if err := transaction.Create(&replacementSources).Error; err != nil {
					return err
				}
			}
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", current.ArtifactID).
			Updates(map[string]any{"updated_at": now, "version": gorm.Expr("version + 1")}).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "artifact.draft_updated", "draft", draftID, map[string]any{"sequence": nextSequence}); err != nil {
			return err
		}
		return enqueue(transaction, "artifact", current.ArtifactID.String(), "artifact.draft_updated", "worksflow.artifact.draft.updated", map[string]any{
			"artifactId": current.ArtifactID.String(), "draftId": draftID, "sequence": nextSequence,
		})
	})
	if err != nil {
		return ArtifactDraft{}, err
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return ArtifactDraft{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	current.Sequence = nextSequence
	current.ETag = nextETag
	current.SchemaVersion = input.SchemaVersion
	current.ContentRef = contentRef.ID
	current.ContentHash = contentRef.ContentHash
	current.ByteSize = contentRef.ByteSize
	current.UpdatedBy = actorUUID
	current.UpdatedAt = now
	if input.SourceVersions == nil {
		replacementSources, err = s.loadDraftSourceModels(ctx, draftUUID)
		if err != nil {
			return ArtifactDraft{}, err
		}
	}
	return draftFromModel(current, cloneJSON(input.Content), sourcesFromModels(replacementSources)), nil
}

func (s *ArtifactService) CreateRevision(ctx context.Context, artifactID, actorID, expectedDraftETag string, input CreateRevisionInput) (ArtifactRevision, error) {
	artifactUUID, projectID, err := s.authorizeArtifact(ctx, artifactID, actorID, ActionEdit)
	if err != nil {
		return ArtifactRevision{}, err
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return ArtifactRevision{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	input.ChangeSummary = strings.TrimSpace(input.ChangeSummary)
	if len(input.ChangeSummary) > 2000 {
		return ArtifactRevision{}, fmt.Errorf("%w: change summary", ErrInvalidInput)
	}
	if input.ChangeSource == "" {
		input.ChangeSource = "human"
	}
	if !validChangeSource(input.ChangeSource) {
		return ArtifactRevision{}, fmt.Errorf("%w: change source", ErrInvalidInput)
	}
	now := s.now().UTC()
	var revisionModel storage.ArtifactRevisionModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var artifact storage.ArtifactModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", artifactUUID).Take(&artifact).Error; err != nil {
			return err
		}
		if artifact.LatestDraftID == nil {
			return ErrNotFound
		}
		var draft storage.ArtifactDraftModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", *artifact.LatestDraftID).Take(&draft).Error; err != nil {
			return err
		}
		if expectedDraftETag == "" || draft.ETag != expectedDraftETag || draft.Status != "draft" {
			return ErrConflict
		}
		if artifact.LatestRevisionID != nil {
			var previous storage.ArtifactRevisionModel
			if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", *artifact.LatestRevisionID).Take(&previous).Error; err != nil {
				return err
			}
			if previous.ContentHash == draft.ContentHash {
				return fmt.Errorf("%w: draft has no changes since the latest revision", ErrConflict)
			}
			if previous.WorkflowStatus == "in_review" {
				if err := transaction.Model(&storage.ReviewRequestModel{}).
					Where("revision_id = ? AND status = 'open'", previous.ID).
					Updates(map[string]any{"status": "stale", "closed_at": now}).Error; err != nil {
					return err
				}
				if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", previous.ID).
					Update("workflow_status", "changes_requested").Error; err != nil {
					return err
				}
			}
		}
		var latest uint64
		if err := transaction.Model(&storage.ArtifactRevisionModel{}).
			Where("artifact_id = ?", artifactUUID).Select("COALESCE(MAX(revision_number), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		revisionID := uuid.New()
		revisionModel = storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: artifactUUID, RevisionNumber: latest + 1,
			ParentRevisionID: artifact.LatestRevisionID, SchemaVersion: draft.SchemaVersion,
			ContentStore: draft.ContentStore, ContentRef: draft.ContentRef, ContentHash: draft.ContentHash,
			ByteSize: draft.ByteSize, WorkflowStatus: "draft", ChangeSource: input.ChangeSource,
			ChangeSummary: input.ChangeSummary, CreatedBy: actorUUID, CreatedAt: now,
		}
		if err := transaction.Create(&revisionModel).Error; err != nil {
			return err
		}
		var draftSources []storage.ArtifactDraftSourceModel
		if err := transaction.Where("draft_id = ?", draft.ID).Find(&draftSources).Error; err != nil {
			return err
		}
		for _, source := range draftSources {
			targetRevisionID := revisionID
			dependency := storage.ArtifactDependencyModel{
				ID: uuid.New(), ProjectID: projectID, SourceArtifactID: source.SourceArtifactID,
				SourceRevisionID: source.SourceRevisionID, SourceContentHash: source.SourceContentHash,
				TargetArtifactID: artifactUUID, TargetRevisionID: &targetRevisionID,
				Relation: "derives_from", Required: source.Required, CreatedBy: actorUUID, CreatedAt: now,
			}
			if err := transaction.Create(&dependency).Error; err != nil {
				return err
			}
			if source.SourceAnchorID != nil {
				link := storage.TraceLinkModel{
					ID: uuid.New(), ProjectID: projectID, SourceArtifactID: source.SourceArtifactID,
					SourceRevisionID: source.SourceRevisionID, SourceAnchorID: source.SourceAnchorID,
					TargetArtifactID: artifactUUID, TargetRevisionID: &targetRevisionID,
					Relation: "derives_from", Metadata: json.RawMessage(`{}`),
					CreatedBy: actorUUID, CreatedAt: now,
				}
				if err := transaction.Create(&link).Error; err != nil {
					return err
				}
			}
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", artifactUUID).
			Updates(map[string]any{"latest_revision_id": revisionID, "updated_at": now, "version": gorm.Expr("version + 1")}).Error; err != nil {
			return err
		}
		if err := transaction.Model(&storage.ArtifactDraftModel{}).Where("id = ?", draft.ID).
			Update("base_revision_id", revisionID).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectID, actorUUID, "artifact.revision_created", "revision", revisionID.String(), map[string]any{"artifactId": artifactID, "revisionNumber": latest + 1}); err != nil {
			return err
		}
		return enqueue(transaction, "artifact", artifactID, "artifact.revision_created", "worksflow.artifact.revision.created", map[string]any{
			"projectId": projectID.String(), "artifactId": artifactID, "revisionId": revisionID.String(),
		})
	})
	if err != nil {
		return ArtifactRevision{}, err
	}
	return s.loadRevision(ctx, revisionModel.ID, true)
}

func (s *ArtifactService) ListRevisions(ctx context.Context, artifactID, actorID string) ([]ArtifactRevision, error) {
	artifactUUID, _, err := s.authorizeArtifact(ctx, artifactID, actorID, ActionView)
	if err != nil {
		return nil, err
	}
	var models []storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("artifact_id = ?", artifactUUID).
		Order("revision_number DESC").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]ArtifactRevision, 0, len(models))
	for _, model := range models {
		revision, err := s.revisionWithContent(ctx, model, false)
		if err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	return result, nil
}

func (s *ArtifactService) GetRevision(ctx context.Context, revisionID, actorID string) (ArtifactRevision, error) {
	revisionUUID, err := uuid.Parse(revisionID)
	if err != nil {
		return ArtifactRevision{}, fmt.Errorf("%w: revision id", ErrInvalidInput)
	}
	var model storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ?", revisionUUID).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ArtifactRevision{}, ErrNotFound
		}
		return ArtifactRevision{}, err
	}
	if _, _, err := s.authorizeArtifact(ctx, model.ArtifactID.String(), actorID, ActionView); err != nil {
		return ArtifactRevision{}, err
	}
	return s.revisionWithContent(ctx, model, true)
}

func (s *ArtifactService) authorizeArtifact(ctx context.Context, artifactID, actorID string, action Action) (uuid.UUID, uuid.UUID, error) {
	artifactUUID, err := uuid.Parse(artifactID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: artifact id", ErrInvalidInput)
	}
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Select("id", "project_id").Where("id = ?", artifactUUID).Take(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, uuid.Nil, ErrNotFound
		}
		return uuid.Nil, uuid.Nil, err
	}
	if _, err := s.access.Authorize(ctx, artifact.ProjectID.String(), actorID, action); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return artifactUUID, artifact.ProjectID, nil
}

func (s *ArtifactService) loadDraft(ctx context.Context, draftID uuid.UUID, includeContent bool) (ArtifactDraft, error) {
	var model storage.ArtifactDraftModel
	if err := s.database.WithContext(ctx).Where("id = ?", draftID).Take(&model).Error; err != nil {
		return ArtifactDraft{}, err
	}
	var payload json.RawMessage
	if includeContent {
		stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
		if err != nil {
			return ArtifactDraft{}, err
		}
		payload = stored.Payload
	}
	sources, err := s.loadDraftSourceModels(ctx, model.ID)
	if err != nil {
		return ArtifactDraft{}, err
	}
	return draftFromModel(model, payload, sourcesFromModels(sources)), nil
}

func (s *ArtifactService) loadRevision(ctx context.Context, revisionID uuid.UUID, includeContent bool) (ArtifactRevision, error) {
	var model storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ?", revisionID).Take(&model).Error; err != nil {
		return ArtifactRevision{}, err
	}
	return s.revisionWithContent(ctx, model, includeContent)
}

func (s *ArtifactService) revisionWithContent(ctx context.Context, model storage.ArtifactRevisionModel, includeContent bool) (ArtifactRevision, error) {
	var payload json.RawMessage
	if includeContent {
		stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
		if err != nil {
			return ArtifactRevision{}, err
		}
		payload = stored.Payload
	}
	return revisionFromModel(model, payload), nil
}

type artifactStatusFields struct {
	SyncStatus     *string
	DeliveryStatus *string
	RevisionStatus *string
}

func artifactFromModels(model storage.ArtifactModel, fields *artifactStatusFields) Artifact {
	status := "draft"
	syncStatus := "current"
	deliveryStatus := "incomplete"
	if model.Lifecycle == "archived" {
		status = "archived"
	} else if fields != nil && fields.RevisionStatus != nil {
		switch *fields.RevisionStatus {
		case "approved":
			status = "approved"
		case "in_review":
			status = "inReview"
		case "changes_requested":
			status = "changesRequested"
		case "superseded":
			if model.LatestApprovedRevisionID != nil {
				status = "approved"
			}
		}
	} else if model.LatestApprovedRevisionID != nil {
		status = "approved"
	}
	if fields != nil {
		if fields.SyncStatus != nil && *fields.SyncStatus != "" {
			syncStatus = *fields.SyncStatus
			if model.Lifecycle != "archived" && (syncStatus == "needs_sync" || syncStatus == "blocked") {
				status = "needsSync"
			}
		}
		if fields.DeliveryStatus != nil && *fields.DeliveryStatus != "" {
			deliveryStatus = *fields.DeliveryStatus
		}
	}
	return Artifact{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), Kind: model.Kind,
		ArtifactKey: model.ArtifactKey, Title: model.Title, Lifecycle: model.Lifecycle,
		Status: status, SyncStatus: syncStatus, DeliveryStatus: deliveryStatus,
		LatestDraftID: uuidStringPointer(model.LatestDraftID), LatestRevisionID: uuidStringPointer(model.LatestRevisionID),
		LatestApprovedRevisionID: uuidStringPointer(model.LatestApprovedRevisionID),
		Version:                  model.Version, ETag: artifactETag(model.ID, model.Version),
		CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
}

func draftFromModel(model storage.ArtifactDraftModel, payload json.RawMessage, sources []ArtifactSource) ArtifactDraft {
	return ArtifactDraft{
		ID: model.ID.String(), ArtifactID: model.ArtifactID.String(), BaseRevisionID: uuidStringPointer(model.BaseRevisionID),
		Sequence: model.Sequence, SchemaVersion: model.SchemaVersion, Content: payload,
		ContentHash: model.ContentHash, SourceVersions: sources, Status: model.Status, CreatedBy: model.CreatedBy.String(),
		UpdatedBy: model.UpdatedBy.String(), CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt, ETag: model.ETag,
	}
}

func (s *ArtifactService) validateSourceModels(ctx context.Context, projectID, draftID, actorID uuid.UUID, inputs []ArtifactSourceInput) ([]storage.ArtifactDraftSourceModel, error) {
	trace := &TraceService{database: s.database, contents: s.contents}
	result := make([]storage.ArtifactDraftSourceModel, 0, len(inputs))
	seen := map[string]bool{}
	for _, input := range inputs {
		artifactID, revisionID, err := trace.validateRef(ctx, projectID, input.Ref)
		if err != nil {
			return nil, err
		}
		input.Purpose = strings.TrimSpace(input.Purpose)
		if input.Purpose == "" || len(input.Purpose) > 240 {
			return nil, fmt.Errorf("%w: source purpose", ErrInvalidInput)
		}
		key := revisionID.String() + "\x00" + input.Purpose
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate source", ErrInvalidInput)
		}
		seen[key] = true
		result = append(result, storage.ArtifactDraftSourceModel{
			DraftID: draftID, SourceArtifactID: artifactID, SourceRevisionID: revisionID,
			SourceContentHash: input.Ref.ContentHash, SourceAnchorID: input.Ref.AnchorID, Purpose: input.Purpose,
			Required: input.Required, AddedBy: actorID, AddedAt: s.now().UTC(),
		})
	}
	return result, nil
}

func (s *ArtifactService) loadDraftSourceModels(ctx context.Context, draftID uuid.UUID) ([]storage.ArtifactDraftSourceModel, error) {
	var sources []storage.ArtifactDraftSourceModel
	if err := s.database.WithContext(ctx).Where("draft_id = ?", draftID).
		Order("added_at ASC").Find(&sources).Error; err != nil {
		return nil, err
	}
	return sources, nil
}

func sourcesFromModels(models []storage.ArtifactDraftSourceModel) []ArtifactSource {
	result := make([]ArtifactSource, 0, len(models))
	for _, model := range models {
		result = append(result, ArtifactSource{
			VersionRef: VersionRef{ArtifactID: model.SourceArtifactID.String(), RevisionID: model.SourceRevisionID.String(), ContentHash: model.SourceContentHash, AnchorID: model.SourceAnchorID},
			Purpose:    model.Purpose, Required: model.Required,
		})
	}
	return result
}

func revisionFromModel(model storage.ArtifactRevisionModel, payload json.RawMessage) ArtifactRevision {
	return ArtifactRevision{
		ID: model.ID.String(), ArtifactID: model.ArtifactID.String(), RevisionNumber: model.RevisionNumber,
		ParentRevisionID: uuidStringPointer(model.ParentRevisionID), SchemaVersion: model.SchemaVersion,
		Content: payload, ContentHash: model.ContentHash, WorkflowStatus: model.WorkflowStatus,
		ChangeSource: model.ChangeSource, ChangeSummary: model.ChangeSummary,
		SourceManifestID: uuidStringPointer(model.SourceManifestID), ProposalID: uuidStringPointer(model.ProposalID),
		CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt, ApprovedAt: model.ApprovedAt,
	}
}

func normalizeArtifactKey(value, kind string, id uuid.UUID) string {
	value = artifactKeyCharacters.ReplaceAllString(strings.ToUpper(strings.TrimSpace(value)), "-")
	value = strings.Trim(value, "-")
	if value == "" {
		value = artifactKeyCharacters.ReplaceAllString(strings.ToUpper(kind), "-") + "-" + strings.ToUpper(id.String()[:8])
	}
	if len(value) > 96 {
		value = value[:96]
	}
	return value
}

func artifactETag(artifactID uuid.UUID, version uint64) string {
	return fmt.Sprintf(`"artifact:%s:%d"`, artifactID, version)
}

func uuidStringPointer(value *uuid.UUID) *string {
	if value == nil {
		return nil
	}
	encoded := value.String()
	return &encoded
}

func validChangeSource(value string) bool {
	switch value {
	case "human", "ai_proposal", "import", "merge", "rollback", "system":
		return true
	default:
		return false
	}
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	result := make(json.RawMessage, len(value))
	copy(result, value)
	return result
}
