package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type VersionRef struct {
	ArtifactID  string  `json:"artifactId"`
	RevisionID  string  `json:"revisionId"`
	ContentHash string  `json:"contentHash"`
	AnchorID    *string `json:"anchorId,omitempty"`
}

type TraceLink struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"projectId"`
	Source    VersionRef      `json:"source"`
	Target    VersionRef      `json:"target"`
	Relation  string          `json:"relation"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedBy string          `json:"createdBy"`
	CreatedAt time.Time       `json:"createdAt"`
}

type CreateTraceLinkInput struct {
	Source   VersionRef      `json:"source"`
	Target   VersionRef      `json:"target"`
	Relation string          `json:"relation"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type ArtifactDependency struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Source    VersionRef `json:"source"`
	Target    VersionRef `json:"target"`
	Relation  string     `json:"relation"`
	Required  bool       `json:"required"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
}

type CreateDependencyInput struct {
	Source   VersionRef `json:"source"`
	Target   VersionRef `json:"target"`
	Relation string     `json:"relation"`
	Required bool       `json:"required"`
}

type TraceService struct {
	database *gorm.DB
	access   *AccessControl
	contents content.Store
	now      func() time.Time
}

var traceRelations = map[string]struct{}{
	"drives": {}, "satisfied_by": {}, "contains": {}, "navigates_to": {},
	"uses": {}, "calls": {}, "reads": {}, "writes": {}, "requires": {},
	"realized_by": {}, "implemented_by": {}, "verified_by": {}, "derives_from": {},
	"compiled_into": {},
}

func NewTraceService(database *gorm.DB, access *AccessControl, stores ...content.Store) (*TraceService, error) {
	if database == nil || access == nil {
		return nil, errors.New("trace database and access control are required")
	}
	var contents content.Store
	if len(stores) > 0 {
		contents = stores[0]
	}
	return &TraceService{database: database, access: access, contents: contents, now: time.Now}, nil
}

func (s *TraceService) CreateLink(ctx context.Context, projectID, actorID string, input CreateTraceLinkInput) (TraceLink, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return TraceLink{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return TraceLink{}, err
	}
	sourceArtifact, sourceRevision, err := s.validateRef(ctx, projectUUID, input.Source)
	if err != nil {
		return TraceLink{}, err
	}
	targetArtifact, targetRevision, err := s.validateRef(ctx, projectUUID, input.Target)
	if err != nil {
		return TraceLink{}, err
	}
	input.Relation = strings.TrimSpace(input.Relation)
	if _, valid := traceRelations[input.Relation]; !valid || sourceArtifact == targetArtifact && sourceRevision == targetRevision && stringPointerEqual(input.Source.AnchorID, input.Target.AnchorID) {
		return TraceLink{}, fmt.Errorf("%w: trace relation or self-link", ErrInvalidInput)
	}
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	} else {
		var metadata map[string]any
		if json.Unmarshal(input.Metadata, &metadata) != nil {
			return TraceLink{}, fmt.Errorf("%w: trace metadata", ErrInvalidInput)
		}
	}
	now := s.now().UTC()
	model := storage.TraceLinkModel{
		ID: uuid.New(), ProjectID: projectUUID, SourceArtifactID: sourceArtifact,
		SourceRevisionID: sourceRevision, SourceAnchorID: input.Source.AnchorID,
		TargetArtifactID: targetArtifact, TargetRevisionID: &targetRevision,
		TargetAnchorID: input.Target.AnchorID, Relation: input.Relation,
		Metadata: input.Metadata, CreatedBy: actorUUID, CreatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "trace.created", "trace_link", model.ID.String(), map[string]any{"relation": input.Relation}); err != nil {
			return err
		}
		return enqueue(transaction, "trace", model.ID.String(), "trace.created", "worksflow.trace.created", map[string]any{
			"projectId": projectID, "traceId": model.ID.String(),
		})
	})
	if err != nil {
		return TraceLink{}, err
	}
	return traceFromModel(model, input.Source.ContentHash, input.Target.ContentHash), nil
}

func (s *TraceService) CreateDependency(ctx context.Context, projectID, actorID string, input CreateDependencyInput) (ArtifactDependency, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return ArtifactDependency{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return ArtifactDependency{}, err
	}
	sourceArtifact, sourceRevision, err := s.validateRef(ctx, projectUUID, input.Source)
	if err != nil {
		return ArtifactDependency{}, err
	}
	targetArtifact, targetRevision, err := s.validateRef(ctx, projectUUID, input.Target)
	if err != nil {
		return ArtifactDependency{}, err
	}
	input.Relation = strings.TrimSpace(input.Relation)
	if _, valid := traceRelations[input.Relation]; !valid || sourceArtifact == targetArtifact {
		return ArtifactDependency{}, fmt.Errorf("%w: dependency relation or self-dependency", ErrInvalidInput)
	}
	now := s.now().UTC()
	model := storage.ArtifactDependencyModel{
		ID: uuid.New(), ProjectID: projectUUID, SourceArtifactID: sourceArtifact,
		SourceRevisionID: sourceRevision, SourceContentHash: input.Source.ContentHash,
		TargetArtifactID: targetArtifact, TargetRevisionID: &targetRevision,
		Relation: input.Relation, Required: input.Required, CreatedBy: actorUUID, CreatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&model).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		if input.Required {
			var existing int64
			if err := transaction.Model(&storage.TraceLinkModel{}).Where(
				"project_id = ? AND source_artifact_id = ? AND source_revision_id = ? AND source_anchor_id IS NULL AND target_artifact_id = ? AND target_revision_id = ? AND target_anchor_id IS NULL AND relation = ?",
				projectUUID, sourceArtifact, sourceRevision, targetArtifact, targetRevision, input.Relation,
			).Count(&existing).Error; err != nil {
				return err
			}
			if existing == 0 {
				trace := storage.TraceLinkModel{
					ID: uuid.New(), ProjectID: projectUUID,
					SourceArtifactID: sourceArtifact, SourceRevisionID: sourceRevision,
					TargetArtifactID: targetArtifact, TargetRevisionID: &targetRevision,
					Relation: input.Relation, Metadata: json.RawMessage(`{"origin":"required_dependency"}`),
					CreatedBy: actorUUID, CreatedAt: now,
				}
				if err := transaction.Create(&trace).Error; err != nil {
					return err
				}
			}
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "dependency.created", "artifact_dependency", model.ID.String(), map[string]any{"relation": input.Relation}); err != nil {
			return err
		}
		return enqueue(transaction, "artifact", targetArtifact.String(), "dependency.created", "worksflow.artifact.dependency.created", map[string]any{
			"projectId": projectID, "dependencyId": model.ID.String(),
		})
	})
	if err != nil {
		return ArtifactDependency{}, err
	}
	return dependencyFromModel(model, input.Target.ContentHash), nil
}

func (s *TraceService) ListLinks(ctx context.Context, projectID, actorID, artifactID string) ([]TraceLink, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	query := s.database.WithContext(ctx).Where("project_id = ?", projectUUID)
	if artifactID != "" {
		artifactUUID, err := uuid.Parse(artifactID)
		if err != nil {
			return nil, fmt.Errorf("%w: artifact id", ErrInvalidInput)
		}
		query = query.Where("source_artifact_id = ? OR target_artifact_id = ?", artifactUUID, artifactUUID)
	}
	var models []storage.TraceLinkModel
	if err := query.Order("created_at ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]TraceLink, 0, len(models))
	for _, model := range models {
		sourceHash, targetHash, err := s.linkHashes(ctx, model.SourceRevisionID, model.TargetRevisionID)
		if err != nil {
			return nil, err
		}
		result = append(result, traceFromModel(model, sourceHash, targetHash))
	}
	return result, nil
}

func (s *TraceService) ListDependencies(ctx context.Context, artifactID, actorID string) ([]ArtifactDependency, error) {
	artifactUUID, _, err := (&ArtifactService{database: s.database, access: s.access}).authorizeArtifact(ctx, artifactID, actorID, ActionView)
	if err != nil {
		return nil, err
	}
	var models []storage.ArtifactDependencyModel
	if err := s.database.WithContext(ctx).
		Where("source_artifact_id = ? OR target_artifact_id = ?", artifactUUID, artifactUUID).
		Order("created_at ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]ArtifactDependency, 0, len(models))
	for _, model := range models {
		targetHash := ""
		if model.TargetRevisionID != nil {
			var revision storage.ArtifactRevisionModel
			if err := s.database.WithContext(ctx).Select("content_hash").Where("id = ?", *model.TargetRevisionID).Take(&revision).Error; err != nil {
				return nil, err
			}
			targetHash = revision.ContentHash
		}
		result = append(result, dependencyFromModel(model, targetHash))
	}
	return result, nil
}

func (s *TraceService) validateRef(ctx context.Context, projectID uuid.UUID, reference VersionRef) (uuid.UUID, uuid.UUID, error) {
	artifactID, err := uuid.Parse(reference.ArtifactID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: reference artifact id", ErrInvalidInput)
	}
	revisionID, err := uuid.Parse(reference.RevisionID)
	if err != nil || !strings.HasPrefix(reference.ContentHash, "sha256:") {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: reference revision or hash", ErrInvalidInput)
	}
	var revision storage.ArtifactRevisionModel
	err = s.database.WithContext(ctx).Table("artifact_revisions").
		Select("artifact_revisions.*").
		Joins("JOIN artifacts ON artifacts.id = artifact_revisions.artifact_id").
		Where("artifact_revisions.id = ? AND artifact_revisions.artifact_id = ? AND artifacts.project_id = ?", revisionID, artifactID, projectID).
		Take(&revision).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if revision.ContentHash != reference.ContentHash {
		return uuid.Nil, uuid.Nil, ErrConflict
	}
	if reference.AnchorID != nil && strings.TrimSpace(*reference.AnchorID) != "" && s.contents != nil {
		stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		if !containsStableAnchor(stored.Payload, strings.TrimSpace(*reference.AnchorID)) {
			return uuid.Nil, uuid.Nil, fmt.Errorf("%w: reference anchor", ErrNotFound)
		}
	}
	return artifactID, revisionID, nil
}

func containsStableAnchor(payload json.RawMessage, expected string) bool {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return false
	}
	var visit func(any) bool
	visit = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range []string{"id", "key", "businessKey", "requirementId", "acceptanceCriterionId", "layerId", "stateId", "frameId"} {
				if value, ok := typed[key].(string); ok && value == expected {
					return true
				}
			}
			for _, child := range typed {
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(value)
}

func (s *TraceService) linkHashes(ctx context.Context, source uuid.UUID, target *uuid.UUID) (string, string, error) {
	var sourceRevision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Select("content_hash").Where("id = ?", source).Take(&sourceRevision).Error; err != nil {
		return "", "", err
	}
	targetHash := ""
	if target != nil {
		var targetRevision storage.ArtifactRevisionModel
		if err := s.database.WithContext(ctx).Select("content_hash").Where("id = ?", *target).Take(&targetRevision).Error; err != nil {
			return "", "", err
		}
		targetHash = targetRevision.ContentHash
	}
	return sourceRevision.ContentHash, targetHash, nil
}

func traceFromModel(model storage.TraceLinkModel, sourceHash, targetHash string) TraceLink {
	targetRevision := ""
	if model.TargetRevisionID != nil {
		targetRevision = model.TargetRevisionID.String()
	}
	return TraceLink{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(),
		Source:   VersionRef{ArtifactID: model.SourceArtifactID.String(), RevisionID: model.SourceRevisionID.String(), ContentHash: sourceHash, AnchorID: model.SourceAnchorID},
		Target:   VersionRef{ArtifactID: model.TargetArtifactID.String(), RevisionID: targetRevision, ContentHash: targetHash, AnchorID: model.TargetAnchorID},
		Relation: model.Relation, Metadata: cloneJSON(model.Metadata), CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt,
	}
}

func dependencyFromModel(model storage.ArtifactDependencyModel, targetHash string) ArtifactDependency {
	targetRevision := ""
	if model.TargetRevisionID != nil {
		targetRevision = model.TargetRevisionID.String()
	}
	return ArtifactDependency{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(),
		Source:   VersionRef{ArtifactID: model.SourceArtifactID.String(), RevisionID: model.SourceRevisionID.String(), ContentHash: model.SourceContentHash},
		Target:   VersionRef{ArtifactID: model.TargetArtifactID.String(), RevisionID: targetRevision, ContentHash: targetHash},
		Relation: model.Relation, Required: model.Required, CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt,
	}
}

func stringPointerEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
