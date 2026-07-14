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

var systemManagedArtifactKinds = map[string]struct{}{
	"requirement_baseline": {},
	"workspace":            {},
	"quality_report":       {},
	"test_report":          {},
}

func ensureArtifactHealthRow(transaction *gorm.DB, artifactID uuid.UUID, computedAt time.Time) error {
	return transaction.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "artifact_id"}},
		DoNothing: true,
	}).Create(&storage.ArtifactHealthModel{
		ArtifactID: artifactID, SyncStatus: "current", DeliveryStatus: "incomplete",
		Report: json.RawMessage(`{}`), ComputedAt: computedAt,
	}).Error
}

func IsSystemManagedArtifactKind(kind string) bool {
	_, systemManaged := systemManagedArtifactKinds[strings.TrimSpace(kind)]
	return systemManaged
}

func ensureGenericArtifactMutationAllowed(kind string) error {
	if IsSystemManagedArtifactKind(kind) {
		return fmt.Errorf("%w: %s artifacts are system-managed", ErrForbidden, kind)
	}
	return nil
}

func ensureGenericArtifactKeyAllowed(artifactKey string) error {
	normalized := strings.ToUpper(strings.TrimSpace(artifactKey))
	if normalized == "REQUIREMENT-BASELINE" || normalized == "WORKSPACE-MAIN" {
		return fmt.Errorf("%w: artifact key is reserved for a system-managed artifact", ErrForbidden)
	}
	return nil
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
	ID               string           `json:"id"`
	ArtifactID       string           `json:"artifactId"`
	RevisionNumber   uint64           `json:"revisionNumber"`
	ParentRevisionID *string          `json:"basedOnRevisionId,omitempty"`
	SourceVersions   []ArtifactSource `json:"sourceVersions,omitempty"`
	SchemaVersion    int              `json:"schemaVersion"`
	Content          json.RawMessage  `json:"content"`
	ContentHash      string           `json:"contentHash"`
	WorkflowStatus   string           `json:"status"`
	ChangeSource     string           `json:"changeSource"`
	ChangeSummary    string           `json:"changeSummary"`
	SourceManifestID *string          `json:"sourceManifestId,omitempty"`
	ProposalID       *string          `json:"proposalId,omitempty"`
	CreatedBy        string           `json:"createdBy"`
	CreatedAt        time.Time        `json:"createdAt"`
	ApprovedAt       *time.Time       `json:"approvedAt,omitempty"`
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

type SystemRevisionSource struct {
	Ref      VersionRef
	Purpose  string
	Required bool
	Relation string
}

func PersistSystemRevisionLineage(
	transaction *gorm.DB,
	projectID uuid.UUID,
	targetArtifactID uuid.UUID,
	targetRevisionID uuid.UUID,
	targetDraftID uuid.UUID,
	actorID uuid.UUID,
	createdAt time.Time,
	sources []SystemRevisionSource,
) ([]ArtifactSource, error) {
	if transaction == nil {
		return nil, errors.New("revision lineage transaction is required")
	}
	revisionSources := make([]storage.ArtifactRevisionSourceModel, 0, len(sources))
	draftSources := make([]storage.ArtifactDraftSourceModel, 0, len(sources))
	dependencies := make([]storage.ArtifactDependencyModel, 0, len(sources))
	dependencyIndexes := make(map[string]int, len(sources))
	links := make([]storage.TraceLinkModel, 0, len(sources)*2)
	seenSources := make(map[string]struct{}, len(sources))
	seenLinks := make(map[string]struct{}, len(sources)*2)
	frozen := make([]ArtifactSource, 0, len(sources))
	for ordinal, source := range sources {
		source.Purpose = strings.TrimSpace(source.Purpose)
		source.Relation = strings.TrimSpace(source.Relation)
		sourceArtifactID, err := uuid.Parse(source.Ref.ArtifactID)
		if err != nil {
			return nil, fmt.Errorf("%w: system revision source artifact", ErrInvalidInput)
		}
		sourceRevisionID, err := uuid.Parse(source.Ref.RevisionID)
		if err != nil || !strings.HasPrefix(source.Ref.ContentHash, "sha256:") || source.Purpose == "" {
			return nil, fmt.Errorf("%w: system revision source", ErrInvalidInput)
		}
		var exactRevision storage.ArtifactRevisionModel
		if err := transaction.Table("artifact_revisions").
			Select("artifact_revisions.*").
			Joins("JOIN artifacts ON artifacts.id = artifact_revisions.artifact_id").
			Where(
				"artifact_revisions.id = ? AND artifact_revisions.artifact_id = ? AND artifacts.project_id = ?",
				sourceRevisionID, sourceArtifactID, projectID,
			).
			Take(&exactRevision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if exactRevision.ContentHash != source.Ref.ContentHash {
			return nil, ErrConflict
		}
		if _, valid := traceRelations[source.Relation]; !valid {
			return nil, fmt.Errorf("%w: system revision source relation", ErrInvalidInput)
		}
		var anchorID *string
		if source.Ref.AnchorID != nil {
			anchor := strings.TrimSpace(*source.Ref.AnchorID)
			if anchor != "" {
				anchorID = &anchor
			}
		}
		sourceKey := sourceRevisionID.String() + "\x00" + source.Purpose
		if _, duplicate := seenSources[sourceKey]; duplicate {
			return nil, fmt.Errorf("%w: duplicate system revision source", ErrInvalidInput)
		}
		seenSources[sourceKey] = struct{}{}
		revisionSources = append(revisionSources, storage.ArtifactRevisionSourceModel{
			RevisionID: targetRevisionID, Ordinal: ordinal, SourceArtifactID: sourceArtifactID,
			SourceRevisionID: sourceRevisionID, SourceContentHash: source.Ref.ContentHash,
			SourceAnchorID: cloneStringPointer(anchorID), Purpose: source.Purpose,
			Required: source.Required, AddedBy: actorID, AddedAt: createdAt,
		})
		if targetDraftID != uuid.Nil {
			draftSources = append(draftSources, storage.ArtifactDraftSourceModel{
				DraftID: targetDraftID, SourceArtifactID: sourceArtifactID,
				SourceRevisionID: sourceRevisionID, SourceContentHash: source.Ref.ContentHash,
				SourceAnchorID: cloneStringPointer(anchorID), Purpose: source.Purpose,
				Required: source.Required, AddedBy: actorID, AddedAt: createdAt,
			})
		}
		frozen = append(frozen, ArtifactSource{
			VersionRef: VersionRef{
				ArtifactID: source.Ref.ArtifactID, RevisionID: source.Ref.RevisionID,
				ContentHash: source.Ref.ContentHash, AnchorID: cloneStringPointer(anchorID),
			},
			Purpose: source.Purpose, Required: source.Required,
		})

		dependencyKey := sourceRevisionID.String() + "\x00" + source.Relation
		if sourceArtifactID != targetArtifactID {
			if index, exists := dependencyIndexes[dependencyKey]; exists {
				if source.Required {
					dependencies[index].Required = true
				}
			} else {
				dependencyIndexes[dependencyKey] = len(dependencies)
				revisionID := targetRevisionID
				dependencies = append(dependencies, storage.ArtifactDependencyModel{
					ID: uuid.New(), ProjectID: projectID, SourceArtifactID: sourceArtifactID,
					SourceRevisionID: sourceRevisionID, SourceContentHash: source.Ref.ContentHash,
					TargetArtifactID: targetArtifactID, TargetRevisionID: &revisionID,
					Relation: source.Relation, Required: source.Required, CreatedBy: actorID, CreatedAt: createdAt,
				})
			}
		}
		wholeLinkKey := dependencyKey + "\x00"
		if _, exists := seenLinks[wholeLinkKey]; !exists {
			seenLinks[wholeLinkKey] = struct{}{}
			revisionID := targetRevisionID
			links = append(links, storage.TraceLinkModel{
				ID: uuid.New(), ProjectID: projectID, SourceArtifactID: sourceArtifactID,
				SourceRevisionID: sourceRevisionID, TargetArtifactID: targetArtifactID,
				TargetRevisionID: &revisionID, Relation: source.Relation,
				Metadata:  json.RawMessage(`{"origin":"frozen_revision_source"}`),
				CreatedBy: actorID, CreatedAt: createdAt,
			})
		}
		if anchorID != nil {
			anchorLinkKey := dependencyKey + "\x00" + *anchorID
			if _, exists := seenLinks[anchorLinkKey]; !exists {
				seenLinks[anchorLinkKey] = struct{}{}
				revisionID := targetRevisionID
				links = append(links, storage.TraceLinkModel{
					ID: uuid.New(), ProjectID: projectID, SourceArtifactID: sourceArtifactID,
					SourceRevisionID: sourceRevisionID, SourceAnchorID: cloneStringPointer(anchorID),
					TargetArtifactID: targetArtifactID, TargetRevisionID: &revisionID,
					Relation: source.Relation, Metadata: json.RawMessage(`{"origin":"frozen_revision_source"}`),
					CreatedBy: actorID, CreatedAt: createdAt,
				})
			}
		}
	}
	if len(revisionSources) > 0 {
		if err := transaction.Create(&revisionSources).Error; err != nil {
			return nil, err
		}
	}
	if len(draftSources) > 0 {
		if err := transaction.Create(&draftSources).Error; err != nil {
			return nil, err
		}
	}
	if len(dependencies) > 0 {
		if err := transaction.Create(&dependencies).Error; err != nil {
			return nil, err
		}
	}
	if len(links) > 0 {
		if err := transaction.Create(&links).Error; err != nil {
			return nil, err
		}
	}
	return frozen, nil
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
	return s.create(ctx, projectID, actorID, uuid.New(), input)
}

// CreateWithID is reserved for internal command handlers which persist an
// idempotency record before creating the artifact. A stable ID lets recovery
// find a committed artifact even if the command checkpoint update failed.
// It retains every authorization, kind, lineage, content and audit guard used
// by Create; callers cannot use it to create system-managed artifacts.
func (s *ArtifactService) CreateWithID(
	ctx context.Context,
	projectID, actorID, artifactID string,
	input CreateArtifactInput,
) (VersionedArtifact, error) {
	stableID, err := uuid.Parse(strings.TrimSpace(artifactID))
	if err != nil || stableID == uuid.Nil {
		return VersionedArtifact{}, fmt.Errorf("%w: artifact id", ErrInvalidInput)
	}
	return s.create(ctx, projectID, actorID, stableID, input)
}

func (s *ArtifactService) create(
	ctx context.Context,
	projectID, actorID string,
	artifactID uuid.UUID,
	input CreateArtifactInput,
) (VersionedArtifact, error) {
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
	if err := ensureGenericArtifactMutationAllowed(input.Kind); err != nil {
		return VersionedArtifact{}, err
	}
	if input.SchemaVersion <= 0 {
		input.SchemaVersion = 1
	}
	if len(input.Content) == 0 {
		input.Content = json.RawMessage(`{"schemaVersion":1}`)
	}
	if err := s.validateArtifactLineage(
		ctx, s.database, projectUUID, input.Kind, input.Content, input.SourceVersions,
	); err != nil {
		return VersionedArtifact{}, err
	}
	draftID := uuid.New()
	artifactKey := normalizeArtifactKey(input.ArtifactKey, input.Kind, artifactID)
	if err := ensureGenericArtifactKeyAllowed(artifactKey); err != nil {
		return VersionedArtifact{}, err
	}
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
		if err := ensureArtifactHealthRow(transaction, artifactID, now); err != nil {
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
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where("id = ?", current.ArtifactID).Take(&artifact).Error; err != nil {
		return ArtifactDraft{}, err
	}
	if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
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
	var existingSources []storage.ArtifactDraftSourceModel
	effectiveSources := input.SourceVersions
	if input.SourceVersions == nil {
		existingSources, err = s.loadDraftSourceModels(ctx, draftUUID)
		if err != nil {
			return ArtifactDraft{}, err
		}
		effectiveSources = sourceInputsFromDraftModels(existingSources)
	}
	if err := s.validateArtifactLineage(
		ctx, s.database, projectUUID, artifact.Kind, input.Content, effectiveSources,
	); err != nil {
		return ArtifactDraft{}, err
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
	replacementSources := existingSources
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
			"projectId": projectUUID.String(), "artifactId": current.ArtifactID.String(),
			"draftId": draftID, "sequence": nextSequence,
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
	return draftFromModel(current, cloneJSON(input.Content), sourcesFromModels(replacementSources)), nil
}

func (s *ArtifactService) CreateRevision(ctx context.Context, artifactID, actorID, expectedDraftETag string, input CreateRevisionInput) (ArtifactRevision, error) {
	return s.createRevision(ctx, artifactID, actorID, expectedDraftETag, uuid.New(), input)
}

// CreateRevisionWithID uses the complete normal revision path with a stable
// identity reserved by a durable internal command. It intentionally does not
// turn duplicate IDs into success: recovery must load and validate the exact
// committed revision before proceeding.
func (s *ArtifactService) CreateRevisionWithID(
	ctx context.Context,
	artifactID, actorID, expectedDraftETag, revisionID string,
	input CreateRevisionInput,
) (ArtifactRevision, error) {
	stableID, err := uuid.Parse(strings.TrimSpace(revisionID))
	if err != nil || stableID == uuid.Nil {
		return ArtifactRevision{}, fmt.Errorf("%w: revision id", ErrInvalidInput)
	}
	return s.createRevision(ctx, artifactID, actorID, expectedDraftETag, stableID, input)
}

func (s *ArtifactService) createRevision(
	ctx context.Context,
	artifactID, actorID, expectedDraftETag string,
	revisionID uuid.UUID,
	input CreateRevisionInput,
) (ArtifactRevision, error) {
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
		if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
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
		storedDraft, err := s.contents.Get(ctx, draft.ContentRef, draft.ContentHash)
		if err != nil {
			return err
		}
		var draftSources []storage.ArtifactDraftSourceModel
		if err := transaction.Where("draft_id = ?", draft.ID).
			Order("added_at ASC, source_revision_id ASC, purpose ASC").Find(&draftSources).Error; err != nil {
			return err
		}
		if err := s.validateArtifactLineage(
			ctx,
			transaction,
			projectID,
			artifact.Kind,
			storedDraft.Payload,
			sourceInputsFromDraftModels(draftSources),
		); err != nil {
			return err
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
		// Applying an OutputProposal materializes its changes into the active
		// draft. Freeze that immutable proposal/manifest identity into the first
		// (and any subsequent manual) revision cut from the same draft so workflow
		// gates can prove that a submitted edit actually contains the reviewed AI
		// proposal instead of accepting the proposal base revision itself.
		var appliedProposal storage.OutputProposalModel
		proposalQueryErr := transaction.
			Where("base_draft_id = ? AND artifact_id = ? AND status IN ?", draft.ID, artifactUUID, []string{"applied", "partially_applied"}).
			Order("applied_at DESC, created_at DESC").Take(&appliedProposal).Error
		if proposalQueryErr != nil && !errors.Is(proposalQueryErr, gorm.ErrRecordNotFound) {
			return proposalQueryErr
		}
		var proposalID, sourceManifestID *uuid.UUID
		if proposalQueryErr == nil {
			proposal := appliedProposal.ID
			manifest := appliedProposal.InputManifestID
			proposalID, sourceManifestID = &proposal, &manifest
		}
		revisionModel = storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: artifactUUID, RevisionNumber: latest + 1,
			ParentRevisionID: artifact.LatestRevisionID, SchemaVersion: draft.SchemaVersion,
			ContentStore: draft.ContentStore, ContentRef: draft.ContentRef, ContentHash: draft.ContentHash,
			ByteSize: draft.ByteSize, WorkflowStatus: "draft", ChangeSource: input.ChangeSource,
			ChangeSummary: input.ChangeSummary, SourceManifestID: sourceManifestID, ProposalID: proposalID,
			CreatedBy: actorUUID, CreatedAt: now,
		}
		if err := transaction.Create(&revisionModel).Error; err != nil {
			return err
		}
		frozenSources := revisionSourceModelsFromDraft(revisionID, draftSources)
		if len(frozenSources) > 0 {
			if err := transaction.Create(&frozenSources).Error; err != nil {
				return err
			}
		}
		dependencies, links := revisionLineageModelsFromDraft(
			projectID, artifactUUID, revisionID, actorUUID, now, draftSources,
		)
		for index := range dependencies {
			dependency := dependencies[index]
			if err := transaction.Create(&dependency).Error; err != nil {
				return err
			}
		}
		for index := range links {
			link := links[index]
			if err := transaction.Create(&link).Error; err != nil {
				return err
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
	revisionIDs := make([]uuid.UUID, 0, len(models))
	for _, model := range models {
		revisionIDs = append(revisionIDs, model.ID)
	}
	sourceModels, err := s.loadRevisionSourceModels(ctx, revisionIDs)
	if err != nil {
		return nil, err
	}
	result := make([]ArtifactRevision, 0, len(models))
	for _, model := range models {
		result = append(result, revisionFromModel(
			model,
			nil,
			revisionSourcesFromModels(sourceModels[model.ID]),
		))
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
	sourceModels, err := s.loadRevisionSourceModels(ctx, []uuid.UUID{model.ID})
	if err != nil {
		return ArtifactRevision{}, err
	}
	return revisionFromModel(model, payload, revisionSourcesFromModels(sourceModels[model.ID])), nil
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
		var sourceAnchorID *string
		if input.Ref.AnchorID != nil {
			anchor := strings.TrimSpace(*input.Ref.AnchorID)
			if anchor != "" {
				sourceAnchorID = &anchor
			}
		}
		result = append(result, storage.ArtifactDraftSourceModel{
			DraftID: draftID, SourceArtifactID: artifactID, SourceRevisionID: revisionID,
			SourceContentHash: input.Ref.ContentHash, SourceAnchorID: sourceAnchorID, Purpose: input.Purpose,
			Required: input.Required, AddedBy: actorID, AddedAt: s.now().UTC(),
		})
	}
	return result, nil
}

type resolvedArtifactLineageSource struct {
	Input    ArtifactSourceInput
	Artifact storage.ArtifactModel
	Revision storage.ArtifactRevisionModel
}

func (s *ArtifactService) validateArtifactLineage(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	kind string,
	payload json.RawMessage,
	inputs []ArtifactSourceInput,
) error {
	switch kind {
	case "blueprint":
		return s.validateBlueprintBaselineSources(ctx, database, projectID, payload, inputs)
	case "page_spec":
		return s.validatePageSpecBlueprintSource(ctx, database, projectID, payload, inputs)
	case "prototype":
		return s.validatePrototypePageSpecSource(ctx, database, projectID, payload, inputs, false)
	default:
		return nil
	}
}

func (s *ArtifactService) validateArtifactLineageForReview(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	kind string,
	payload json.RawMessage,
	inputs []ArtifactSourceInput,
) error {
	switch kind {
	case "blueprint":
		return s.validateBlueprintBaselineSources(ctx, database, projectID, payload, inputs, true)
	case "page_spec":
		return s.validatePageSpecBlueprintSource(ctx, database, projectID, payload, inputs, true)
	case "prototype":
		return s.validatePrototypePageSpecSource(ctx, database, projectID, payload, inputs, true)
	default:
		return nil
	}
}

func (s *ArtifactService) validatePageSpecBlueprintSource(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	payload json.RawMessage,
	inputs []ArtifactSourceInput,
	strictValues ...bool,
) error {
	var content map[string]any
	if json.Unmarshal(payload, &content) != nil {
		return fmt.Errorf("%w: PageSpec content must be a JSON object", ErrBlockingGate)
	}
	pageNodeID := strings.TrimSpace(firstString(content, "blueprintPageNodeId"))
	if pageNodeID == "" {
		return fmt.Errorf("%w: PageSpec must declare blueprintPageNodeId", ErrBlockingGate)
	}
	resolved, err := s.resolveArtifactLineageSources(ctx, database, projectID, inputs)
	if err != nil {
		return err
	}
	blueprints := lineageSourcesByKind(resolved, "blueprint")
	if len(blueprints) != 1 {
		return fmt.Errorf("%w: PageSpec requires exactly one approved Blueprint source", ErrBlockingGate)
	}
	source := blueprints[0]
	anchor := ""
	if source.Input.Ref.AnchorID != nil {
		anchor = strings.TrimSpace(*source.Input.Ref.AnchorID)
	}
	if !source.Input.Required ||
		!artifactLineagePurpose(source.Input.Purpose, "blueprint", "delivery_slice_blueprint") ||
		anchor != pageNodeID {
		return fmt.Errorf("%w: PageSpec Blueprint source must be required and anchored to blueprintPageNodeId", ErrBlockingGate)
	}
	if source.Artifact.Lifecycle != "active" || source.Revision.WorkflowStatus != "approved" {
		return fmt.Errorf("%w: PageSpec Blueprint source must be an approved active revision", ErrBlockingGate)
	}
	strict := semanticStrict(strictValues)
	if strict {
		if source.Artifact.LatestApprovedRevisionID == nil || *source.Artifact.LatestApprovedRevisionID != source.Revision.ID {
			return fmt.Errorf("%w: PageSpec must use the current approved Blueprint revision", ErrBlockingGate)
		}
		if err := requireCurrentArtifactHealth(ctx, database, source.Artifact.ID, "PageSpec Blueprint source"); err != nil {
			return err
		}
	}
	stored, err := s.contents.Get(ctx, source.Revision.ContentRef, source.Revision.ContentHash)
	if err != nil {
		return err
	}
	nodes, edges, err := DecodeBlueprintSemanticGraph(stored.Payload)
	if err != nil {
		return fmt.Errorf("%w: invalid Blueprint semantic graph: %v", ErrBlockingGate, err)
	}
	var page *BlueprintSemanticNode
	for index := range nodes {
		if nodes[index].ID == pageNodeID && nodes[index].Kind == "page" {
			page = &nodes[index]
			break
		}
	}
	if page == nil {
		return fmt.Errorf("%w: PageSpec Blueprint anchor must identify an existing Page node", ErrBlockingGate)
	}
	trace, err := s.loadBlueprintRequirementTrace(ctx, database, projectID, source.Revision, strict)
	if err != nil {
		return err
	}
	if err := validatePageSpecSemanticTrace(payload, *page, nodes, edges, trace, strictValues...); err != nil {
		return fmt.Errorf("%w: %v", ErrBlockingGate, err)
	}
	return nil
}

func (s *ArtifactService) validatePrototypePageSpecSource(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	payload json.RawMessage,
	inputs []ArtifactSourceInput,
	requireCoverage bool,
) error {
	var content struct {
		PageSpecRevision VersionRef `json:"pageSpecRevision"`
		Exploratory      bool       `json:"exploratory"`
	}
	if json.Unmarshal(payload, &content) != nil {
		return fmt.Errorf("%w: Prototype content must pin pageSpecRevision", ErrBlockingGate)
	}
	resolved, err := s.resolveArtifactLineageSources(ctx, database, projectID, inputs)
	if err != nil {
		return err
	}
	pageSpecs := lineageSourcesByKind(resolved, "page_spec")
	if len(pageSpecs) != 1 {
		return fmt.Errorf("%w: Prototype requires exactly one PageSpec source", ErrBlockingGate)
	}
	source := pageSpecs[0]
	if !source.Input.Required ||
		!artifactLineagePurpose(source.Input.Purpose, "page_spec", "delivery_slice_page_spec") ||
		hasVersionAnchor(source.Input.Ref) {
		return fmt.Errorf("%w: Prototype PageSpec source must be one required whole revision", ErrBlockingGate)
	}
	if hasVersionAnchor(content.PageSpecRevision) || !sameWholeVersionRef(source.Input.Ref, content.PageSpecRevision) {
		return fmt.Errorf("%w: Prototype content pageSpecRevision must exactly match its PageSpec source", ErrBlockingGate)
	}
	if source.Artifact.Lifecycle != "active" {
		return fmt.Errorf("%w: Prototype PageSpec source must be active", ErrBlockingGate)
	}
	if requireCoverage && content.Exploratory {
		return fmt.Errorf("%w: exploratory Prototype revisions cannot be formally approved", ErrBlockingGate)
	}
	storedPageSpec, err := s.contents.Get(ctx, source.Revision.ContentRef, source.Revision.ContentHash)
	if err != nil {
		return err
	}
	if err := s.validatePrototypeComponentRefs(ctx, database, projectID, payload, resolved, requireCoverage); err != nil {
		return err
	}
	authority, authorityErr := s.loadPrototypeSemanticAuthority(
		ctx, database, projectID, source.Artifact, source.Revision, storedPageSpec.Payload, requireCoverage,
	)
	if authorityErr != nil {
		return authorityErr
	}
	if err := validatePrototypeSemanticTrace(payload, storedPageSpec.Payload, requireCoverage, authority); err != nil {
		return fmt.Errorf("%w: %v", ErrBlockingGate, err)
	}
	if content.Exploratory {
		return nil
	}
	if source.Revision.WorkflowStatus != "approved" || source.Artifact.LatestApprovedRevisionID == nil ||
		*source.Artifact.LatestApprovedRevisionID != source.Revision.ID {
		return fmt.Errorf("%w: formal Prototype requires the current approved PageSpec revision", ErrBlockingGate)
	}
	var health storage.ArtifactHealthModel
	if err := database.WithContext(ctx).Where("artifact_id = ?", source.Artifact.ID).Take(&health).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: formal Prototype PageSpec source has no current health state", ErrBlockingGate)
		}
		return err
	}
	if health.SyncStatus != "current" {
		return fmt.Errorf("%w: formal Prototype requires a current PageSpec source", ErrBlockingGate)
	}
	return nil
}

func artifactLineagePurpose(value string, allowed ...string) bool {
	value = strings.TrimSpace(value)
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (s *ArtifactService) validatePrototypeComponentRefs(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	payload json.RawMessage,
	sources []resolvedArtifactLineageSource,
	strict bool,
) error {
	var prototype map[string]any
	if json.Unmarshal(payload, &prototype) != nil {
		return fmt.Errorf("%w: Prototype content must be a JSON object", ErrBlockingGate)
	}
	allowed := make([]resolvedArtifactLineageSource, 0)
	for _, source := range sources {
		if source.Artifact.Kind != "component_registry" && source.Artifact.Kind != "design_system" {
			continue
		}
		if !source.Input.Required || strings.TrimSpace(source.Input.Purpose) != source.Artifact.Kind ||
			source.Artifact.Lifecycle != "active" || source.Revision.WorkflowStatus != "approved" {
			return fmt.Errorf("%w: Prototype component sources must be required approved component_registry/design_system revisions", ErrBlockingGate)
		}
		if strict {
			if source.Artifact.LatestApprovedRevisionID == nil || *source.Artifact.LatestApprovedRevisionID != source.Revision.ID {
				return fmt.Errorf("%w: Prototype component source must be its current approved revision", ErrBlockingGate)
			}
			if err := requireCurrentArtifactHealth(ctx, database, source.Artifact.ID, "Prototype component source"); err != nil {
				return err
			}
		}
		allowed = append(allowed, source)
	}
	trace := &TraceService{database: database, contents: s.contents}
	for layerID, layer := range prototypeCanonicalLayerObjects(prototype) {
		value, exists := layer["componentRef"]
		if !exists || value == nil {
			continue
		}
		ref, ok := semanticVersionRef(value)
		if !ok {
			return fmt.Errorf("%w: Prototype layer %q has an invalid componentRef", ErrBlockingGate, layerID)
		}
		if _, _, err := trace.validateRef(ctx, projectID, ref); err != nil {
			return fmt.Errorf("%w: Prototype layer %q componentRef is not an exact project revision", ErrBlockingGate, layerID)
		}
		authorized := false
		for _, source := range allowed {
			if source.Input.Ref.ArtifactID != ref.ArtifactID || source.Input.Ref.RevisionID != ref.RevisionID ||
				source.Input.Ref.ContentHash != ref.ContentHash {
				continue
			}
			if hasVersionAnchor(source.Input.Ref) && !stringPointerEqual(source.Input.Ref.AnchorID, ref.AnchorID) {
				continue
			}
			authorized = true
			break
		}
		if !authorized {
			return fmt.Errorf("%w: Prototype layer %q componentRef is not authorized by its immutable component sources", ErrBlockingGate, layerID)
		}
	}
	return nil
}

func (s *ArtifactService) loadPrototypeSemanticAuthority(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	pageSpecArtifact storage.ArtifactModel,
	pageSpecRevision storage.ArtifactRevisionModel,
	pageSpecPayload json.RawMessage,
	strict bool,
) (prototypeSemanticAuthority, error) {
	var pageSpecContent map[string]any
	if json.Unmarshal(pageSpecPayload, &pageSpecContent) != nil {
		return prototypeSemanticAuthority{}, fmt.Errorf("%w: PageSpec content must be a JSON object", ErrBlockingGate)
	}
	if strict {
		if report := ValidateArtifactContent("page_spec", pageSpecPayload); !report.Valid {
			return prototypeSemanticAuthority{}, fmt.Errorf("%w: formal Prototype source PageSpec no longer satisfies its canonical schema", ErrBlockingGate)
		}
	}
	pageNodeID := strings.TrimSpace(firstString(pageSpecContent, "blueprintPageNodeId"))
	if pageNodeID == "" {
		return prototypeSemanticAuthority{}, fmt.Errorf("%w: PageSpec must declare blueprintPageNodeId", ErrBlockingGate)
	}

	var sourceModels []storage.ArtifactRevisionSourceModel
	if err := database.WithContext(ctx).Where("revision_id = ?", pageSpecRevision.ID).
		Order("ordinal ASC").Find(&sourceModels).Error; err != nil {
		return prototypeSemanticAuthority{}, err
	}
	resolved, err := s.resolveArtifactLineageSources(
		ctx, database, projectID, sourceInputsFromRevisionModels(sourceModels),
	)
	if err != nil {
		return prototypeSemanticAuthority{}, err
	}
	blueprints := lineageSourcesByKind(resolved, "blueprint")
	if len(blueprints) != 1 {
		return prototypeSemanticAuthority{}, fmt.Errorf("%w: formal Prototype requires its PageSpec's exact Blueprint source closure", ErrBlockingGate)
	}
	blueprint := blueprints[0]
	anchor := ""
	if blueprint.Input.Ref.AnchorID != nil {
		anchor = strings.TrimSpace(*blueprint.Input.Ref.AnchorID)
	}
	if !blueprint.Input.Required ||
		!artifactLineagePurpose(blueprint.Input.Purpose, "blueprint", "delivery_slice_blueprint") ||
		anchor != pageNodeID ||
		blueprint.Artifact.Lifecycle != "active" || blueprint.Revision.WorkflowStatus != "approved" {
		return prototypeSemanticAuthority{}, fmt.Errorf("%w: formal Prototype PageSpec must pin one approved Blueprint Page source", ErrBlockingGate)
	}
	if strict {
		if blueprint.Artifact.LatestApprovedRevisionID == nil || *blueprint.Artifact.LatestApprovedRevisionID != blueprint.Revision.ID {
			return prototypeSemanticAuthority{}, fmt.Errorf("%w: formal Prototype PageSpec must use the current approved Blueprint revision", ErrBlockingGate)
		}
		if err := requireCurrentArtifactHealth(ctx, database, blueprint.Artifact.ID, "formal Prototype Blueprint source"); err != nil {
			return prototypeSemanticAuthority{}, err
		}
	}
	storedBlueprint, err := s.contents.Get(ctx, blueprint.Revision.ContentRef, blueprint.Revision.ContentHash)
	if err != nil {
		return prototypeSemanticAuthority{}, err
	}
	if strict {
		if report := ValidateArtifactContent("blueprint", storedBlueprint.Payload); !report.Valid {
			return prototypeSemanticAuthority{}, fmt.Errorf("%w: formal Prototype source Blueprint no longer satisfies its canonical schema", ErrBlockingGate)
		}
	}
	nodes, edges, err := DecodeBlueprintSemanticGraph(storedBlueprint.Payload)
	if err != nil {
		return prototypeSemanticAuthority{}, fmt.Errorf("%w: invalid Blueprint semantic graph: %v", ErrBlockingGate, err)
	}
	var page *BlueprintSemanticNode
	for index := range nodes {
		if nodes[index].ID == pageNodeID && nodes[index].Kind == "page" {
			page = &nodes[index]
			break
		}
	}
	if page == nil {
		return prototypeSemanticAuthority{}, fmt.Errorf("%w: formal Prototype PageSpec Blueprint anchor is not a Page", ErrBlockingGate)
	}
	trace, baselineRef, err := s.loadBlueprintRequirementTraceWithRef(ctx, database, projectID, blueprint.Revision, strict)
	if err != nil {
		return prototypeSemanticAuthority{}, err
	}
	if err := validateBlueprintRequirementTrace(storedBlueprint.Payload, trace, strict); err != nil {
		return prototypeSemanticAuthority{}, fmt.Errorf("%w: Prototype source Blueprint has invalid Requirement Baseline trace: %v", ErrBlockingGate, err)
	}
	if err := validatePageSpecSemanticTrace(pageSpecPayload, *page, nodes, edges, trace, strict); err != nil {
		return prototypeSemanticAuthority{}, fmt.Errorf("%w: formal Prototype source PageSpec is semantically incomplete: %v", ErrBlockingGate, err)
	}
	requirementIDs := map[string]bool{}
	for _, requirementID := range page.RequirementIDs {
		requirementIDs[requirementID] = true
	}
	return prototypeSemanticAuthority{
		requirementIDs:     requirementIDs,
		acceptanceIDs:      pageAcceptanceSet(*page, trace),
		pageNodeID:         page.ID,
		pageSpecArtifactID: pageSpecArtifact.ID.String(),
		baselineRef:        baselineRef,
		blueprintRef:       blueprint.Input.Ref,
		pageSpecRef: VersionRef{
			ArtifactID: pageSpecArtifact.ID.String(), RevisionID: pageSpecRevision.ID.String(),
			ContentHash: pageSpecRevision.ContentHash,
		},
	}, nil
}

func (s *ArtifactService) loadBlueprintRequirementTrace(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	blueprintRevision storage.ArtifactRevisionModel,
	strictValues ...bool,
) (requirementTraceSnapshot, error) {
	trace, _, err := s.loadBlueprintRequirementTraceWithRef(ctx, database, projectID, blueprintRevision, strictValues...)
	return trace, err
}

func (s *ArtifactService) loadBlueprintRequirementTraceWithRef(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	blueprintRevision storage.ArtifactRevisionModel,
	strictValues ...bool,
) (requirementTraceSnapshot, VersionRef, error) {
	var models []storage.ArtifactRevisionSourceModel
	if err := database.WithContext(ctx).Where("revision_id = ?", blueprintRevision.ID).
		Order("ordinal ASC").Find(&models).Error; err != nil {
		return requirementTraceSnapshot{}, VersionRef{}, err
	}
	inputs := make([]ArtifactSourceInput, 0, len(models))
	for _, model := range models {
		inputs = append(inputs, ArtifactSourceInput{Ref: VersionRef{
			ArtifactID: model.SourceArtifactID.String(), RevisionID: model.SourceRevisionID.String(),
			ContentHash: model.SourceContentHash, AnchorID: cloneStringPointer(model.SourceAnchorID),
		}, Purpose: model.Purpose, Required: model.Required})
	}
	resolved, err := s.resolveArtifactLineageSources(ctx, database, projectID, inputs)
	if err != nil {
		return requirementTraceSnapshot{}, VersionRef{}, err
	}
	baselines := lineageSourcesByKind(resolved, "requirement_baseline")
	if len(baselines) != 1 || !baselines[0].Input.Required || hasVersionAnchor(baselines[0].Input.Ref) ||
		strings.TrimSpace(baselines[0].Input.Purpose) != "requirement_baseline" ||
		!isExactApprovedRequirementBaseline(baselines[0].Artifact, baselines[0].Revision, baselines[0].Input.Ref) {
		return requirementTraceSnapshot{}, VersionRef{}, fmt.Errorf("%w: Blueprint must pin exactly one whole approved Requirement Baseline", ErrBlockingGate)
	}
	if semanticStrict(strictValues) && !isCurrentApprovedRequirementBaseline(
		baselines[0].Artifact, baselines[0].Revision, baselines[0].Input.Ref,
	) {
		return requirementTraceSnapshot{}, VersionRef{}, fmt.Errorf("%w: Blueprint must pin the current approved Requirement Baseline", ErrBlockingGate)
	}
	if semanticStrict(strictValues) {
		if err := requireCurrentArtifactHealth(ctx, database, baselines[0].Artifact.ID, "Blueprint Requirement Baseline source"); err != nil {
			return requirementTraceSnapshot{}, VersionRef{}, err
		}
	}
	stored, err := s.contents.Get(ctx, baselines[0].Revision.ContentRef, baselines[0].Revision.ContentHash)
	if err != nil {
		return requirementTraceSnapshot{}, VersionRef{}, err
	}
	trace, err := decodeRequirementTrace(stored.Payload)
	if err != nil {
		return requirementTraceSnapshot{}, VersionRef{}, fmt.Errorf("%w: decode Blueprint Requirement Baseline: %v", ErrBlockingGate, err)
	}
	return trace, baselines[0].Input.Ref, nil
}

func (s *ArtifactService) resolveArtifactLineageSources(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	inputs []ArtifactSourceInput,
) ([]resolvedArtifactLineageSource, error) {
	trace := &TraceService{database: database, contents: s.contents}
	result := make([]resolvedArtifactLineageSource, 0, len(inputs))
	for _, input := range inputs {
		artifactID, revisionID, err := trace.validateRef(ctx, projectID, input.Ref)
		if err != nil {
			if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
				return nil, fmt.Errorf("%w: lineage source must be an exact project revision and anchor", ErrBlockingGate)
			}
			return nil, err
		}
		var artifact storage.ArtifactModel
		if err := database.WithContext(ctx).Where("id = ?", artifactID).Take(&artifact).Error; err != nil {
			return nil, err
		}
		var revision storage.ArtifactRevisionModel
		if err := database.WithContext(ctx).Where("id = ? AND artifact_id = ?", revisionID, artifactID).Take(&revision).Error; err != nil {
			return nil, err
		}
		result = append(result, resolvedArtifactLineageSource{Input: input, Artifact: artifact, Revision: revision})
	}
	return result, nil
}

func lineageSourcesByKind(sources []resolvedArtifactLineageSource, kind string) []resolvedArtifactLineageSource {
	result := make([]resolvedArtifactLineageSource, 0, 1)
	for _, source := range sources {
		if source.Artifact.Kind == kind {
			result = append(result, source)
		}
	}
	return result
}

func blueprintContainsPageNode(payload json.RawMessage, pageNodeID string) bool {
	var content map[string]any
	if json.Unmarshal(payload, &content) != nil {
		return false
	}
	nodes := append([]map[string]any(nil), objectSlice(content["nodes"])...)
	if semantic, ok := content["semantic"].(map[string]any); ok {
		nodes = append(nodes, objectSlice(semantic["nodes"])...)
	}
	for _, node := range nodes {
		if strings.TrimSpace(firstString(node, "id")) == pageNodeID && firstString(node, "kind", "type") == "page" {
			return true
		}
	}
	return false
}

func sameWholeVersionRef(left, right VersionRef) bool {
	return !hasVersionAnchor(left) && !hasVersionAnchor(right) &&
		left.ArtifactID == right.ArtifactID && left.RevisionID == right.RevisionID &&
		left.ContentHash == right.ContentHash
}

func hasVersionAnchor(reference VersionRef) bool {
	return reference.AnchorID != nil && strings.TrimSpace(*reference.AnchorID) != ""
}

func (s *ArtifactService) validateBlueprintBaselineSources(
	ctx context.Context,
	database *gorm.DB,
	projectID uuid.UUID,
	payload json.RawMessage,
	inputs []ArtifactSourceInput,
	strictValues ...bool,
) error {
	if len(inputs) != 1 || !inputs[0].Required || inputs[0].Ref.AnchorID != nil ||
		strings.TrimSpace(inputs[0].Purpose) != "requirement_baseline" {
		return fmt.Errorf("%w: Blueprint requires exactly one whole approved Requirement Baseline revision", ErrBlockingGate)
	}
	input := inputs[0]
	trace := &TraceService{database: database, contents: s.contents}
	artifactID, revisionID, err := trace.validateRef(ctx, projectID, input.Ref)
	if err != nil {
		if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
			return fmt.Errorf("%w: Blueprint Requirement Baseline source must be an exact current revision", ErrBlockingGate)
		}
		return err
	}
	var artifact storage.ArtifactModel
	if err := database.WithContext(ctx).Where("id = ?", artifactID).Take(&artifact).Error; err != nil {
		return err
	}
	var revision storage.ArtifactRevisionModel
	if err := database.WithContext(ctx).Where("id = ? AND artifact_id = ?", revisionID, artifactID).Take(&revision).Error; err != nil {
		return err
	}
	if !isCurrentApprovedRequirementBaseline(artifact, revision, input.Ref) {
		return fmt.Errorf("%w: Blueprint source must be the current approved Requirement Baseline revision", ErrBlockingGate)
	}
	if semanticStrict(strictValues) {
		if err := requireCurrentArtifactHealth(ctx, database, artifact.ID, "Blueprint Requirement Baseline source"); err != nil {
			return err
		}
	}
	stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
	if err != nil {
		return err
	}
	requirementTrace, err := decodeRequirementTrace(stored.Payload)
	if err != nil {
		return fmt.Errorf("%w: decode Blueprint Requirement Baseline: %v", ErrBlockingGate, err)
	}
	if err := validateBlueprintRequirementTrace(payload, requirementTrace, strictValues...); err != nil {
		return fmt.Errorf("%w: %v", ErrBlockingGate, err)
	}
	return nil
}

func isCurrentApprovedRequirementBaseline(
	artifact storage.ArtifactModel,
	revision storage.ArtifactRevisionModel,
	reference VersionRef,
) bool {
	return isExactApprovedRequirementBaseline(artifact, revision, reference) &&
		artifact.LatestApprovedRevisionID != nil && *artifact.LatestApprovedRevisionID == revision.ID
}

func isExactApprovedRequirementBaseline(
	artifact storage.ArtifactModel,
	revision storage.ArtifactRevisionModel,
	reference VersionRef,
) bool {
	return artifact.Kind == "requirement_baseline" && artifact.Lifecycle == "active" &&
		revision.ArtifactID == artifact.ID && revision.WorkflowStatus == "approved" &&
		revision.ContentHash == reference.ContentHash && reference.ArtifactID == artifact.ID.String() &&
		reference.RevisionID == revision.ID.String() && reference.AnchorID == nil
}

func requireCurrentArtifactHealth(
	ctx context.Context,
	database *gorm.DB,
	artifactID uuid.UUID,
	label string,
) error {
	var health storage.ArtifactHealthModel
	if err := database.WithContext(ctx).Where("artifact_id = ?", artifactID).Take(&health).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: %s has no current health state", ErrBlockingGate, label)
		}
		return err
	}
	if health.SyncStatus != "current" {
		return fmt.Errorf("%w: %s must be current", ErrBlockingGate, label)
	}
	return nil
}

func sourceInputsFromDraftModels(models []storage.ArtifactDraftSourceModel) []ArtifactSourceInput {
	result := make([]ArtifactSourceInput, 0, len(models))
	for _, model := range models {
		result = append(result, ArtifactSourceInput{
			Ref: VersionRef{
				ArtifactID: model.SourceArtifactID.String(), RevisionID: model.SourceRevisionID.String(),
				ContentHash: model.SourceContentHash, AnchorID: cloneStringPointer(model.SourceAnchorID),
			},
			Purpose: model.Purpose, Required: model.Required,
		})
	}
	return result
}

func (s *ArtifactService) loadDraftSourceModels(ctx context.Context, draftID uuid.UUID) ([]storage.ArtifactDraftSourceModel, error) {
	var sources []storage.ArtifactDraftSourceModel
	if err := s.database.WithContext(ctx).Where("draft_id = ?", draftID).
		Order("added_at ASC, source_revision_id ASC, purpose ASC").Find(&sources).Error; err != nil {
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

func (s *ArtifactService) loadRevisionSourceModels(
	ctx context.Context,
	revisionIDs []uuid.UUID,
) (map[uuid.UUID][]storage.ArtifactRevisionSourceModel, error) {
	result := make(map[uuid.UUID][]storage.ArtifactRevisionSourceModel, len(revisionIDs))
	if len(revisionIDs) == 0 {
		return result, nil
	}
	var models []storage.ArtifactRevisionSourceModel
	if err := s.database.WithContext(ctx).Where("revision_id IN ?", revisionIDs).
		Order("revision_id ASC, ordinal ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	for _, model := range models {
		result[model.RevisionID] = append(result[model.RevisionID], model)
	}
	return result, nil
}

func revisionSourceModelsFromDraft(
	revisionID uuid.UUID,
	models []storage.ArtifactDraftSourceModel,
) []storage.ArtifactRevisionSourceModel {
	result := make([]storage.ArtifactRevisionSourceModel, 0, len(models))
	for ordinal, model := range models {
		result = append(result, storage.ArtifactRevisionSourceModel{
			RevisionID: revisionID, Ordinal: ordinal, SourceArtifactID: model.SourceArtifactID,
			SourceRevisionID: model.SourceRevisionID, SourceContentHash: model.SourceContentHash,
			SourceAnchorID: cloneStringPointer(model.SourceAnchorID), Purpose: model.Purpose,
			Required: model.Required, AddedBy: model.AddedBy, AddedAt: model.AddedAt,
		})
	}
	return result
}

func revisionSourcesFromModels(models []storage.ArtifactRevisionSourceModel) []ArtifactSource {
	result := make([]ArtifactSource, 0, len(models))
	for _, model := range models {
		result = append(result, ArtifactSource{
			VersionRef: VersionRef{
				ArtifactID: model.SourceArtifactID.String(), RevisionID: model.SourceRevisionID.String(),
				ContentHash: model.SourceContentHash, AnchorID: cloneStringPointer(model.SourceAnchorID),
			},
			Purpose: model.Purpose, Required: model.Required,
		})
	}
	return result
}

func sourceInputsFromRevisionModels(models []storage.ArtifactRevisionSourceModel) []ArtifactSourceInput {
	result := make([]ArtifactSourceInput, 0, len(models))
	for _, model := range models {
		result = append(result, ArtifactSourceInput{
			Ref: VersionRef{
				ArtifactID: model.SourceArtifactID.String(), RevisionID: model.SourceRevisionID.String(),
				ContentHash: model.SourceContentHash, AnchorID: cloneStringPointer(model.SourceAnchorID),
			},
			Purpose: model.Purpose, Required: model.Required,
		})
	}
	return result
}

func revisionLineageModelsFromDraft(
	projectID uuid.UUID,
	targetArtifactID uuid.UUID,
	targetRevisionID uuid.UUID,
	actorID uuid.UUID,
	createdAt time.Time,
	sources []storage.ArtifactDraftSourceModel,
) ([]storage.ArtifactDependencyModel, []storage.TraceLinkModel) {
	dependencies := make([]storage.ArtifactDependencyModel, 0, len(sources))
	dependencyIndexes := make(map[uuid.UUID]int, len(sources))
	links := make([]storage.TraceLinkModel, 0, len(sources))
	seenLinks := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		// A prior revision of the same artifact is represented by
		// ParentRevisionID. Emitting an artifact dependency for it violates the
		// graph's distinct source/target invariant and double-counts ancestry.
		if source.SourceArtifactID != targetArtifactID {
			if index, exists := dependencyIndexes[source.SourceRevisionID]; exists {
				if source.Required {
					dependencies[index].Required = true
				}
			} else {
				dependencyIndexes[source.SourceRevisionID] = len(dependencies)
				revisionID := targetRevisionID
				dependencies = append(dependencies, storage.ArtifactDependencyModel{
					ID: uuid.New(), ProjectID: projectID, SourceArtifactID: source.SourceArtifactID,
					SourceRevisionID: source.SourceRevisionID, SourceContentHash: source.SourceContentHash,
					TargetArtifactID: targetArtifactID, TargetRevisionID: &revisionID,
					Relation: "derives_from", Required: source.Required, CreatedBy: actorID, CreatedAt: createdAt,
				})
				wholeLinkKey := source.SourceRevisionID.String() + "\x00"
				seenLinks[wholeLinkKey] = struct{}{}
				links = append(links, storage.TraceLinkModel{
					ID: uuid.New(), ProjectID: projectID, SourceArtifactID: source.SourceArtifactID,
					SourceRevisionID: source.SourceRevisionID, TargetArtifactID: targetArtifactID,
					TargetRevisionID: &revisionID, Relation: "derives_from",
					Metadata:  json.RawMessage(`{"origin":"required_dependency"}`),
					CreatedBy: actorID, CreatedAt: createdAt,
				})
			}
		}
		if source.SourceAnchorID == nil {
			continue
		}
		anchor := strings.TrimSpace(*source.SourceAnchorID)
		if anchor == "" {
			continue
		}
		key := source.SourceRevisionID.String() + "\x00" + anchor
		if _, exists := seenLinks[key]; exists {
			continue
		}
		seenLinks[key] = struct{}{}
		revisionID := targetRevisionID
		links = append(links, storage.TraceLinkModel{
			ID: uuid.New(), ProjectID: projectID, SourceArtifactID: source.SourceArtifactID,
			SourceRevisionID: source.SourceRevisionID, SourceAnchorID: &anchor,
			TargetArtifactID: targetArtifactID, TargetRevisionID: &revisionID,
			Relation: "derives_from", Metadata: json.RawMessage(`{}`),
			CreatedBy: actorID, CreatedAt: createdAt,
		})
	}
	return dependencies, links
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func revisionFromModel(
	model storage.ArtifactRevisionModel,
	payload json.RawMessage,
	sources []ArtifactSource,
) ArtifactRevision {
	return ArtifactRevision{
		ID: model.ID.String(), ArtifactID: model.ArtifactID.String(), RevisionNumber: model.RevisionNumber,
		ParentRevisionID: uuidStringPointer(model.ParentRevisionID), SchemaVersion: model.SchemaVersion,
		SourceVersions: sources, Content: payload, ContentHash: model.ContentHash, WorkflowStatus: model.WorkflowStatus,
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
