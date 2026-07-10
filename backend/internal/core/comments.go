package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CommentMessage struct {
	ID              string     `json:"id"`
	ParentMessageID *string    `json:"parentId,omitempty"`
	Body            string     `json:"body"`
	Mentions        []string   `json:"mentions"`
	CreatedBy       string     `json:"createdBy"`
	CreatedAt       time.Time  `json:"createdAt"`
	EditedAt        *time.Time `json:"editedAt,omitempty"`
}

type CommentThread struct {
	ID         string           `json:"id"`
	ProjectID  string           `json:"projectId"`
	ArtifactID string           `json:"artifactId"`
	RevisionID *string          `json:"revisionId,omitempty"`
	Anchor     json.RawMessage  `json:"anchor"`
	Severity   string           `json:"severity"`
	AssignedTo *string          `json:"assignedTo,omitempty"`
	CreatedBy  string           `json:"createdBy"`
	CreatedAt  time.Time        `json:"createdAt"`
	ResolvedBy *string          `json:"resolvedBy,omitempty"`
	ResolvedAt *time.Time       `json:"resolvedAt,omitempty"`
	OutdatedAt *time.Time       `json:"outdatedAt,omitempty"`
	Messages   []CommentMessage `json:"messages"`
	ETag       string           `json:"etag"`
}

type CreateCommentInput struct {
	RevisionID      *string         `json:"revisionId,omitempty"`
	ParentThreadID  *string         `json:"parentId,omitempty"`
	ParentMessageID *string         `json:"parentMessageId,omitempty"`
	Body            string          `json:"body"`
	Anchor          json.RawMessage `json:"anchor,omitempty"`
	Severity        string          `json:"severity,omitempty"`
	AssignedTo      *string         `json:"assignedTo,omitempty"`
	Mentions        []string        `json:"mentions,omitempty"`
}

type CommentService struct {
	database *gorm.DB
	access   *AccessControl
	now      func() time.Time
}

func NewCommentService(database *gorm.DB, access *AccessControl) (*CommentService, error) {
	if database == nil || access == nil {
		return nil, errors.New("comment database and access control are required")
	}
	return &CommentService{database: database, access: access, now: time.Now}, nil
}

func (s *CommentService) Create(ctx context.Context, artifactID, actorID string, input CreateCommentInput) (CommentThread, error) {
	artifactUUID, projectUUID, err := (&ArtifactService{database: s.database, access: s.access}).authorizeArtifact(ctx, artifactID, actorID, ActionComment)
	if err != nil {
		return CommentThread{}, err
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return CommentThread{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	input.Body = strings.TrimSpace(input.Body)
	if input.Body == "" || len(input.Body) > 20_000 {
		return CommentThread{}, fmt.Errorf("%w: comment body", ErrInvalidInput)
	}
	if input.Severity == "" {
		input.Severity = "normal"
	}
	if input.Severity != "normal" && input.Severity != "blocking" {
		return CommentThread{}, fmt.Errorf("%w: comment severity", ErrInvalidInput)
	}
	if input.Severity == "blocking" {
		if _, err := s.access.Authorize(ctx, projectUUID.String(), actorID, ActionReview); err != nil {
			return CommentThread{}, err
		}
	}
	mentions, mentionPayload, err := parseMentionIDs(input.Mentions)
	if err != nil {
		return CommentThread{}, err
	}
	var revisionUUID *uuid.UUID
	if input.RevisionID != nil {
		parsed, err := uuid.Parse(*input.RevisionID)
		if err != nil {
			return CommentThread{}, fmt.Errorf("%w: revision id", ErrInvalidInput)
		}
		var count int64
		if err := s.database.WithContext(ctx).Model(&storage.ArtifactRevisionModel{}).
			Where("id = ? AND artifact_id = ?", parsed, artifactUUID).Count(&count).Error; err != nil {
			return CommentThread{}, err
		}
		if count != 1 {
			return CommentThread{}, ErrNotFound
		}
		revisionUUID = &parsed
	}
	var assignedTo *uuid.UUID
	if input.AssignedTo != nil {
		parsed, err := uuid.Parse(*input.AssignedTo)
		if err != nil {
			return CommentThread{}, fmt.Errorf("%w: assigned user id", ErrInvalidInput)
		}
		if err := ensureProjectMember(s.database.WithContext(ctx), projectUUID, parsed); err != nil {
			return CommentThread{}, err
		}
		assignedTo = &parsed
	}
	if len(input.Anchor) == 0 {
		input.Anchor = json.RawMessage(`{}`)
	} else {
		var anchor map[string]any
		if json.Unmarshal(input.Anchor, &anchor) != nil {
			return CommentThread{}, fmt.Errorf("%w: comment anchor", ErrInvalidInput)
		}
	}
	now := s.now().UTC()
	thread := storage.CommentThreadModel{
		ID: uuid.New(), ProjectID: projectUUID, ArtifactID: artifactUUID, RevisionID: revisionUUID,
		Anchor: input.Anchor, Severity: input.Severity, AssignedTo: assignedTo,
		CreatedBy: actorUUID, CreatedAt: now,
	}
	message := storage.CommentMessageModel{
		ID: uuid.New(), ThreadID: thread.ID, Body: input.Body, Mentions: mentionPayload,
		CreatedBy: actorUUID, CreatedAt: now,
	}
	if input.ParentThreadID != nil {
		parsedThread, err := uuid.Parse(*input.ParentThreadID)
		if err != nil {
			return CommentThread{}, fmt.Errorf("%w: parent thread id", ErrInvalidInput)
		}
		thread.ID = parsedThread
		message.ThreadID = parsedThread
		if input.ParentMessageID != nil {
			parsedMessage, err := uuid.Parse(*input.ParentMessageID)
			if err != nil {
				return CommentThread{}, fmt.Errorf("%w: parent message id", ErrInvalidInput)
			}
			message.ParentMessageID = &parsedMessage
		}
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if input.ParentThreadID == nil {
			if err := transaction.Create(&thread).Error; err != nil {
				return err
			}
		} else {
			if err := transaction.Clauses(clause.Locking{Strength: "KEY SHARE"}).
				Where("id = ? AND artifact_id = ?", thread.ID, artifactUUID).Take(&thread).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrNotFound
				}
				return err
			}
			if thread.ResolvedAt != nil {
				return ErrConflict
			}
			if message.ParentMessageID != nil {
				var count int64
				if err := transaction.Model(&storage.CommentMessageModel{}).
					Where("id = ? AND thread_id = ?", *message.ParentMessageID, thread.ID).Count(&count).Error; err != nil {
					return err
				}
				if count != 1 {
					return ErrNotFound
				}
			}
		}
		if err := transaction.Create(&message).Error; err != nil {
			return err
		}
		for _, mentionedUser := range mentions {
			if mentionedUser == actorUUID {
				continue
			}
			if err := ensureProjectMember(transaction, projectUUID, mentionedUser); err != nil {
				return err
			}
			notification := storage.NotificationModel{
				ID: uuid.New(), UserID: mentionedUser, ProjectID: projectUUID,
				Kind: "comment", Title: "You were mentioned in a comment", Body: truncate(input.Body, 240),
				ResourceType: "comment_thread", ResourceID: thread.ID.String(), CreatedAt: now,
			}
			if err := transaction.Create(&notification).Error; err != nil {
				return err
			}
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "comment.created", "comment_thread", thread.ID.String(), map[string]any{"artifactId": artifactID}); err != nil {
			return err
		}
		return enqueue(transaction, "comment", thread.ID.String(), "comment.created", "worksflow.comment.created", map[string]any{
			"projectId": projectUUID.String(), "artifactId": artifactID, "threadId": thread.ID.String(), "messageId": message.ID.String(),
		})
	})
	if err != nil {
		return CommentThread{}, err
	}
	return s.Get(ctx, thread.ID.String(), actorID)
}

func (s *CommentService) Get(ctx context.Context, threadID, actorID string) (CommentThread, error) {
	threadUUID, err := uuid.Parse(threadID)
	if err != nil {
		return CommentThread{}, fmt.Errorf("%w: comment thread id", ErrInvalidInput)
	}
	var thread storage.CommentThreadModel
	if err := s.database.WithContext(ctx).Where("id = ?", threadUUID).Take(&thread).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CommentThread{}, ErrNotFound
		}
		return CommentThread{}, err
	}
	if _, err := s.access.Authorize(ctx, thread.ProjectID.String(), actorID, ActionView); err != nil {
		return CommentThread{}, err
	}
	return s.threadFromModel(ctx, thread)
}

func (s *CommentService) ListArtifact(ctx context.Context, artifactID, actorID string) ([]CommentThread, error) {
	artifactUUID, _, err := (&ArtifactService{database: s.database, access: s.access}).authorizeArtifact(ctx, artifactID, actorID, ActionView)
	if err != nil {
		return nil, err
	}
	var models []storage.CommentThreadModel
	if err := s.database.WithContext(ctx).Where("artifact_id = ?", artifactUUID).
		Order("created_at DESC").Find(&models).Error; err != nil {
		return nil, err
	}
	return s.threadsFromModels(ctx, models)
}

func (s *CommentService) ListProject(ctx context.Context, projectID, actorID string) ([]CommentThread, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	var models []storage.CommentThreadModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectUUID).
		Order("created_at DESC").Find(&models).Error; err != nil {
		return nil, err
	}
	return s.threadsFromModels(ctx, models)
}

func (s *CommentService) SetResolved(ctx context.Context, threadID, actorID string, resolved bool) (CommentThread, error) {
	return s.SetResolvedIfMatch(ctx, threadID, actorID, "", resolved)
}

func (s *CommentService) SetResolvedIfMatch(ctx context.Context, threadID, actorID, expectedETag string, resolved bool) (CommentThread, error) {
	threadUUID, err := uuid.Parse(threadID)
	if err != nil {
		return CommentThread{}, fmt.Errorf("%w: comment thread id", ErrInvalidInput)
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return CommentThread{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	var thread storage.CommentThreadModel
	now := s.now().UTC()
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", threadUUID).Take(&thread).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if expectedETag != "" {
			var messageCount int64
			if err := transaction.Model(&storage.CommentMessageModel{}).
				Where("thread_id = ? AND deleted_at IS NULL", thread.ID).Count(&messageCount).Error; err != nil {
				return err
			}
			if commentEntityTag(thread, messageCount) != expectedETag {
				return ErrConflict
			}
		}
		if _, err := (&AccessControl{database: transaction}).Authorize(ctx, thread.ProjectID.String(), actorID, ActionComment); err != nil {
			return err
		}
		if thread.Severity == "blocking" {
			if _, err := (&AccessControl{database: transaction}).Authorize(ctx, thread.ProjectID.String(), actorID, ActionReview); err != nil {
				return err
			}
		}
		updates := map[string]any{}
		if resolved {
			updates["resolved_at"] = now
			updates["resolved_by"] = actorUUID
			thread.ResolvedAt = &now
			thread.ResolvedBy = &actorUUID
		} else {
			updates["resolved_at"] = nil
			updates["resolved_by"] = nil
			thread.ResolvedAt = nil
			thread.ResolvedBy = nil
		}
		if err := transaction.Model(&storage.CommentThreadModel{}).Where("id = ?", threadUUID).Updates(updates).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, thread.ProjectID, actorUUID, "comment.resolution_changed", "comment_thread", threadID, map[string]any{"resolved": resolved}); err != nil {
			return err
		}
		return enqueue(transaction, "comment", threadID, "comment.resolution_changed", "worksflow.comment.resolution.changed", map[string]any{
			"projectId": thread.ProjectID.String(), "artifactId": thread.ArtifactID.String(), "threadId": threadID, "resolved": resolved,
		})
	})
	if err != nil {
		return CommentThread{}, err
	}
	return s.threadFromModel(ctx, thread)
}

func (s *CommentService) threadsFromModels(ctx context.Context, models []storage.CommentThreadModel) ([]CommentThread, error) {
	result := make([]CommentThread, 0, len(models))
	for _, model := range models {
		thread, err := s.threadFromModel(ctx, model)
		if err != nil {
			return nil, err
		}
		result = append(result, thread)
	}
	return result, nil
}

func (s *CommentService) threadFromModel(ctx context.Context, model storage.CommentThreadModel) (CommentThread, error) {
	var messages []storage.CommentMessageModel
	if err := s.database.WithContext(ctx).Where("thread_id = ? AND deleted_at IS NULL", model.ID).
		Order("created_at ASC").Find(&messages).Error; err != nil {
		return CommentThread{}, err
	}
	result := CommentThread{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), ArtifactID: model.ArtifactID.String(),
		RevisionID: uuidStringPointer(model.RevisionID), Anchor: cloneJSON(model.Anchor), Severity: model.Severity,
		AssignedTo: uuidStringPointer(model.AssignedTo), CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt,
		ResolvedBy: uuidStringPointer(model.ResolvedBy), ResolvedAt: model.ResolvedAt, OutdatedAt: model.OutdatedAt,
		Messages: make([]CommentMessage, 0, len(messages)),
		ETag:     commentEntityTag(model, int64(len(messages))),
	}
	for _, message := range messages {
		var mentions []string
		if json.Unmarshal(message.Mentions, &mentions) != nil {
			mentions = []string{}
		}
		result.Messages = append(result.Messages, CommentMessage{
			ID: message.ID.String(), ParentMessageID: uuidStringPointer(message.ParentMessageID),
			Body: message.Body, Mentions: mentions, CreatedBy: message.CreatedBy.String(),
			CreatedAt: message.CreatedAt, EditedAt: message.EditedAt,
		})
	}
	return result, nil
}

func commentEntityTag(model storage.CommentThreadModel, messageCount int64) string {
	return fmt.Sprintf(`"comment:%s:%d:%d"`, model.ID, messageCount, nullableTimeUnix(model.ResolvedAt))
}

func parseMentionIDs(values []string) ([]uuid.UUID, json.RawMessage, error) {
	result := make([]uuid.UUID, 0, len(values))
	canonical := make([]string, 0, len(values))
	seen := map[uuid.UUID]bool{}
	for _, value := range values {
		parsed, err := uuid.Parse(value)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: mention user id", ErrInvalidInput)
		}
		if seen[parsed] {
			continue
		}
		seen[parsed] = true
		result = append(result, parsed)
		canonical = append(canonical, parsed.String())
	}
	payload, err := json.Marshal(canonical)
	return result, payload, err
}

func ensureProjectMember(database *gorm.DB, projectID, userID uuid.UUID) error {
	var count int64
	if err := database.Model(&storage.ProjectMemberModel{}).
		Where("project_id = ? AND user_id = ?", projectID, userID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func nullableTimeUnix(value *time.Time) int64 {
	if value == nil {
		return 0
	}
	return value.UnixNano()
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
