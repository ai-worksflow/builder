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

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Lifecycle   string    `json:"lifecycle"`
	Role        Role      `json:"role"`
	Version     uint64    `json:"version"`
	ETag        string    `json:"etag"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type CreateProjectInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type UpdateProjectInput struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Lifecycle   *string `json:"lifecycle,omitempty"`
}

type CreatedProject struct {
	Project                Project `json:"project"`
	InitialArtifactID      string  `json:"initialArtifactId"`
	InitialArtifactDraftID string  `json:"initialArtifactDraftId"`
}

type ProjectService struct {
	database     *gorm.DB
	contents     content.Store
	access       *AccessControl
	initializers []ProjectInitializer
	now          func() time.Time
}

type ProjectInitializer interface {
	InitializeProject(context.Context, *gorm.DB, uuid.UUID, uuid.UUID, time.Time) error
}

func NewProjectService(database *gorm.DB, contents content.Store, access *AccessControl, initializers ...ProjectInitializer) (*ProjectService, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("project database, content store and access control are required")
	}
	for _, initializer := range initializers {
		if initializer == nil {
			return nil, errors.New("project initializer must not be nil")
		}
	}
	return &ProjectService{database: database, contents: contents, access: access, initializers: append([]ProjectInitializer(nil), initializers...), now: time.Now}, nil
}

func (s *ProjectService) Create(ctx context.Context, actorID string, input CreateProjectInput) (CreatedProject, error) {
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return CreatedProject{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || len(input.Name) > 160 || len(input.Description) > 4000 {
		return CreatedProject{}, fmt.Errorf("%w: project name or description", ErrInvalidInput)
	}

	projectID := uuid.New()
	artifactID := uuid.New()
	draftID := uuid.New()
	brief := initialProjectBrief(input.Name, input.Description)
	briefPayload, err := json.Marshal(brief)
	if err != nil {
		return CreatedProject{}, err
	}
	contentRef, err := s.contents.PutPending(
		ctx, projectID.String(), "artifact_draft", draftID.String(), 1, briefPayload,
	)
	if err != nil {
		return CreatedProject{}, fmt.Errorf("store initial project brief: %w", err)
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()

	now := s.now().UTC()
	projectModel := storage.ProjectModel{
		ID: projectID, Name: input.Name, Description: input.Description,
		Lifecycle: "active", Version: 1, CreatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
	}
	memberModel := storage.ProjectMemberModel{
		ProjectID: projectID, UserID: actorUUID, Role: string(RoleOwner),
		JoinedAt: now, UpdatedAt: now,
	}
	artifactModel := storage.ArtifactModel{
		ID: artifactID, ProjectID: projectID, Kind: "project_brief", ArtifactKey: "DOC-PROJECT-BRIEF",
		Title: "Project Brief", Lifecycle: "active", Version: 1, LatestDraftID: &draftID,
		CreatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
	}
	draftModel := storage.ArtifactDraftModel{
		ID: draftID, ArtifactID: artifactID, Sequence: 1, ETag: draftETag(draftID, 1, contentRef.ContentHash),
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: contentRef.ID,
		ContentHash: contentRef.ContentHash, ByteSize: contentRef.ByteSize, Status: "draft",
		CreatedBy: actorUUID, UpdatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&projectModel).Error; err != nil {
			return err
		}
		if err := transaction.Create(&memberModel).Error; err != nil {
			return err
		}
		// The artifact pointer references the draft, so insert the artifact without
		// the pointer first and attach it after the draft exists.
		latestDraftID := artifactModel.LatestDraftID
		artifactModel.LatestDraftID = nil
		if err := transaction.Create(&artifactModel).Error; err != nil {
			return err
		}
		if err := transaction.Create(&draftModel).Error; err != nil {
			return err
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
			Update("latest_draft_id", latestDraftID).Error; err != nil {
			return err
		}
		artifactModel.LatestDraftID = latestDraftID
		for _, initializer := range s.initializers {
			if err := initializer.InitializeProject(ctx, transaction, projectID, actorUUID, now); err != nil {
				return err
			}
		}
		if err := insertAudit(transaction, projectID, actorUUID, "project.created", "project", projectID.String(), map[string]any{
			"initialArtifactId": artifactID.String(),
		}); err != nil {
			return err
		}
		return enqueue(transaction, "project", projectID.String(), "project.created", "worksflow.project.created", map[string]any{
			"projectId": projectID.String(), "createdBy": actorID,
		})
	})
	if err != nil {
		return CreatedProject{}, fmt.Errorf("create project transaction: %w", err)
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return CreatedProject{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return CreatedProject{
		Project:           projectFromModel(projectModel, RoleOwner),
		InitialArtifactID: artifactID.String(), InitialArtifactDraftID: draftID.String(),
	}, nil
}

func (s *ProjectService) List(ctx context.Context, actorID string) ([]Project, error) {
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return nil, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	type projectRow struct {
		storage.ProjectModel
		CurrentRole string `gorm:"column:current_role"`
	}
	var rows []projectRow
	err = s.database.WithContext(ctx).Table("projects").
		Select("projects.*, project_members.role AS current_role").
		Joins("JOIN project_members ON project_members.project_id = projects.id").
		Where("project_members.user_id = ?", actorUUID).
		Order("projects.updated_at DESC").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	projects := make([]Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectFromModel(row.ProjectModel, Role(row.CurrentRole)))
	}
	return projects, nil
}

func (s *ProjectService) Get(ctx context.Context, projectID, actorID string) (Project, error) {
	role, err := s.access.Authorize(ctx, projectID, actorID, ActionView)
	if err != nil {
		return Project{}, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return Project{}, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	var model storage.ProjectModel
	err = s.database.WithContext(ctx).Where("id = ?", projectUUID).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("load project: %w", err)
	}
	return projectFromModel(model, role), nil
}

func (s *ProjectService) Update(ctx context.Context, projectID, actorID string, expectedVersion uint64, input UpdateProjectInput) (Project, error) {
	requiredAction := ActionEdit
	if input.Lifecycle != nil {
		requiredAction = ActionAdmin
	}
	role, err := s.access.Authorize(ctx, projectID, actorID, requiredAction)
	if err != nil {
		return Project{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return Project{}, err
	}
	updates := map[string]any{"updated_at": s.now().UTC(), "version": gorm.Expr("version + 1")}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || len(name) > 160 {
			return Project{}, fmt.Errorf("%w: project name", ErrInvalidInput)
		}
		updates["name"] = name
	}
	if input.Description != nil {
		description := strings.TrimSpace(*input.Description)
		if len(description) > 4000 {
			return Project{}, fmt.Errorf("%w: project description", ErrInvalidInput)
		}
		updates["description"] = description
	}
	if input.Lifecycle != nil {
		if *input.Lifecycle != "active" && *input.Lifecycle != "archived" {
			return Project{}, fmt.Errorf("%w: project lifecycle", ErrInvalidInput)
		}
		updates["lifecycle"] = *input.Lifecycle
		if *input.Lifecycle == "archived" {
			updates["archived_at"] = s.now().UTC()
		} else {
			updates["archived_at"] = nil
		}
	}
	if len(updates) == 2 {
		return s.Get(ctx, projectID, actorID)
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&storage.ProjectModel{}).
			Where("id = ? AND version = ?", projectUUID, expectedVersion).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "project.updated", "project", projectID, nil); err != nil {
			return err
		}
		return enqueue(transaction, "project", projectID, "project.updated", "worksflow.project.updated", map[string]any{
			"projectId": projectID, "updatedBy": actorID,
		})
	})
	if err != nil {
		return Project{}, err
	}
	return s.Get(ctx, projectID, actorIDWithRole(actorID, role))
}

func actorIDWithRole(actorID string, _ Role) string { return actorID }

func projectFromModel(model storage.ProjectModel, role Role) Project {
	return Project{
		ID: model.ID.String(), Name: model.Name, Description: model.Description,
		Lifecycle: model.Lifecycle, Role: role, Version: model.Version,
		ETag: projectETag(model.ID, model.Version), CreatedBy: model.CreatedBy.String(),
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
}

func projectETag(projectID uuid.UUID, version uint64) string {
	return fmt.Sprintf(`"project:%s:%d"`, projectID.String(), version)
}

func draftETag(draftID uuid.UUID, sequence uint64, hash string) string {
	shortHash := strings.TrimPrefix(hash, "sha256:")
	if len(shortHash) > 16 {
		shortHash = shortHash[:16]
	}
	return fmt.Sprintf(`"draft:%s:%d:%s"`, draftID.String(), sequence, shortHash)
}

func initialProjectBrief(name, description string) map[string]any {
	background := description
	if background == "" {
		background = "Describe the problem, target users, constraints, and desired outcome with the AI interviewer."
	}
	return map[string]any{
		"schemaVersion":      1,
		"kind":               "projectBrief",
		"summary":            background,
		"requirements":       []any{},
		"acceptanceCriteria": []any{},
		"openQuestions":      []any{},
		"assumptions":        []any{},
		"blocks": []map[string]any{
			{"id": "goal-1", "type": "goal", "text": name + ": " + background, "data": map[string]any{"provenance": "human"}},
			{"id": "question-1", "type": "openQuestion", "text": "Who is the primary user?", "blocking": true, "status": "open", "data": map[string]any{"provenance": "system"}},
			{"id": "question-2", "type": "openQuestion", "text": "What measurable outcome defines success?", "blocking": true, "status": "open", "data": map[string]any{"provenance": "system"}},
		},
	}
}

func insertAudit(transaction *gorm.DB, projectID, actorID uuid.UUID, action, targetType, targetID string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var requestID *string
	if value := RequestIDFromContext(transaction.Statement.Context); value != "" {
		requestID = &value
	}
	return transaction.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: &projectID, ActorID: &actorID, Action: action,
		RequestID: requestID, TargetType: targetType, TargetID: targetID,
		Metadata: payload, CreatedAt: time.Now().UTC(),
	}).Error
}

func enqueue(transaction *gorm.DB, aggregateType, aggregateID, eventType, subject string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return transaction.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: aggregateType, AggregateID: aggregateID,
		EventType: eventType, Subject: subject, Payload: encoded, Headers: json.RawMessage(`{}`),
		AvailableAt: time.Now().UTC(), CreatedAt: time.Now().UTC(),
	}).Error
}

func insertNotification(transaction *gorm.DB, userID, projectID uuid.UUID, kind, title, body, resourceType, resourceID string) error {
	if userID == uuid.Nil {
		return nil
	}
	return transaction.Create(&storage.NotificationModel{
		ID: uuid.New(), UserID: userID, ProjectID: projectID, Kind: kind,
		Title: title, Body: truncate(body, 2000), ResourceType: resourceType,
		ResourceID: resourceID, CreatedAt: time.Now().UTC(),
	}).Error
}
