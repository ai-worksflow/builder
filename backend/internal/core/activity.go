package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type Notification struct {
	ID           string     `json:"id"`
	UserID       string     `json:"userId"`
	ProjectID    string     `json:"projectId"`
	Kind         string     `json:"kind"`
	Title        string     `json:"title"`
	Message      string     `json:"message"`
	ResourceType string     `json:"resourceType"`
	ResourceID   string     `json:"resourceId"`
	CreatedAt    time.Time  `json:"createdAt"`
	ReadAt       *time.Time `json:"readAt,omitempty"`
	ETag         string     `json:"etag"`
}

type AuditEvent struct {
	ID         string          `json:"id"`
	ProjectID  *string         `json:"projectId,omitempty"`
	ActorID    *string         `json:"actorId,omitempty"`
	RequestID  *string         `json:"requestId,omitempty"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Metadata   json.RawMessage `json:"metadata"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type Presence struct {
	ProjectID  string     `json:"projectId"`
	User       MemberUser `json:"user"`
	ArtifactID *string    `json:"artifactId,omitempty"`
	State      string     `json:"state"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
}

type ActivityService struct {
	database    *gorm.DB
	cache       redis.UniversalClient
	access      *AccessControl
	now         func() time.Time
	presenceTTL time.Duration
}

func NewActivityService(database *gorm.DB, cache redis.UniversalClient, access *AccessControl, presenceTTL time.Duration) (*ActivityService, error) {
	if database == nil || access == nil {
		return nil, errors.New("activity database and access control are required")
	}
	if presenceTTL <= 0 {
		presenceTTL = 60 * time.Second
	}
	return &ActivityService{database: database, cache: cache, access: access, now: time.Now, presenceTTL: presenceTTL}, nil
}

func (s *ActivityService) ListNotifications(ctx context.Context, actorID, projectID string, unreadOnly bool) ([]Notification, error) {
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return nil, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	query := s.database.WithContext(ctx).Where("user_id = ?", actorUUID)
	if projectID != "" {
		if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
			return nil, err
		}
		projectUUID, err := uuid.Parse(projectID)
		if err != nil {
			return nil, fmt.Errorf("%w: project id", ErrInvalidInput)
		}
		query = query.Where("project_id = ?", projectUUID)
	}
	if unreadOnly {
		query = query.Where("read_at IS NULL")
	}
	var models []storage.NotificationModel
	if err := query.Order("created_at DESC").Limit(500).Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]Notification, 0, len(models))
	for _, model := range models {
		result = append(result, notificationFromModel(model))
	}
	return result, nil
}

func (s *ActivityService) MarkNotification(ctx context.Context, notificationID, actorID, expectedETag string, read bool) (Notification, error) {
	notificationUUID, err := uuid.Parse(notificationID)
	if err != nil {
		return Notification{}, fmt.Errorf("%w: notification id", ErrInvalidInput)
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return Notification{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	var model storage.NotificationModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Where("id = ? AND user_id = ?", notificationUUID, actorUUID).Take(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if expectedETag == "" || notificationETag(model) != expectedETag {
			return ErrConflict
		}
		var readAt *time.Time
		if read {
			now := s.now().UTC()
			readAt = &now
		}
		if err := transaction.Model(&storage.NotificationModel{}).Where("id = ? AND user_id = ?", notificationUUID, actorUUID).
			Update("read_at", readAt).Error; err != nil {
			return err
		}
		model.ReadAt = readAt
		return nil
	})
	if err != nil {
		return Notification{}, err
	}
	return notificationFromModel(model), nil
}

func (s *ActivityService) ListAudit(ctx context.Context, projectID, actorID string) ([]AuditEvent, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionAdmin); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	var models []storage.AuditEventModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectUUID).
		Order("created_at DESC").Limit(1000).Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]AuditEvent, 0, len(models))
	for _, model := range models {
		result = append(result, AuditEvent{
			ID: model.ID.String(), ProjectID: uuidStringPointer(model.ProjectID), ActorID: uuidStringPointer(model.ActorID),
			RequestID: model.RequestID, Action: model.Action, TargetType: model.TargetType,
			TargetID: model.TargetID, Metadata: cloneJSON(model.Metadata), CreatedAt: model.CreatedAt,
		})
	}
	return result, nil
}

func (s *ActivityService) HeartbeatPresence(ctx context.Context, projectID, actorID string, artifactID *string) (Presence, error) {
	if s.cache == nil {
		return Presence{}, errors.New("presence cache is unavailable")
	}
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return Presence{}, err
	}
	projectUUID, userUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return Presence{}, err
	}
	if artifactID != nil {
		_, artifactProjectID, err := (&ArtifactService{database: s.database, access: s.access}).authorizeArtifact(ctx, *artifactID, actorID, ActionView)
		if err != nil {
			return Presence{}, err
		}
		if artifactProjectID != projectUUID {
			return Presence{}, ErrNotFound
		}
	}
	var user storage.UserModel
	if err := s.database.WithContext(ctx).Where("id = ?", userUUID).Take(&user).Error; err != nil {
		return Presence{}, err
	}
	now := s.now().UTC()
	presence := Presence{
		ProjectID: projectID, User: MemberUser{ID: actorID, Email: user.Email, DisplayName: user.DisplayName, AvatarURL: user.AvatarURL, CreatedAt: user.CreatedAt},
		ArtifactID: artifactID, State: "active", UpdatedAt: now, ExpiresAt: now.Add(s.presenceTTL),
	}
	payload, err := json.Marshal(presence)
	if err != nil {
		return Presence{}, err
	}
	presenceKey := presenceValueKey(projectID, actorID)
	indexKey := presenceIndexKey(projectID)
	pipeline := s.cache.TxPipeline()
	pipeline.Set(ctx, presenceKey, payload, s.presenceTTL)
	pipeline.ZAdd(ctx, indexKey, redis.Z{Score: float64(presence.ExpiresAt.UnixMilli()), Member: actorID})
	pipeline.Expire(ctx, indexKey, s.presenceTTL+time.Minute)
	if _, err := pipeline.Exec(ctx); err != nil {
		return Presence{}, fmt.Errorf("save presence: %w", err)
	}
	return presence, nil
}

func (s *ActivityService) ListPresence(ctx context.Context, projectID, actorID string) ([]Presence, error) {
	if s.cache == nil {
		return nil, errors.New("presence cache is unavailable")
	}
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	indexKey := presenceIndexKey(projectID)
	if err := s.cache.ZRemRangeByScore(ctx, indexKey, "-inf", strconv.FormatInt(now.UnixMilli(), 10)).Err(); err != nil {
		return nil, err
	}
	userIDs, err := s.cache.ZRange(ctx, indexKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return []Presence{}, nil
	}
	keys := make([]string, len(userIDs))
	for index, userID := range userIDs {
		keys[index] = presenceValueKey(projectID, userID)
	}
	values, err := s.cache.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	result := make([]Presence, 0, len(values))
	for _, value := range values {
		encoded, ok := value.(string)
		if !ok {
			continue
		}
		var presence Presence
		if json.Unmarshal([]byte(encoded), &presence) == nil && presence.ExpiresAt.After(now) {
			result = append(result, presence)
		}
	}
	return result, nil
}

func notificationFromModel(model storage.NotificationModel) Notification {
	return Notification{
		ID: model.ID.String(), UserID: model.UserID.String(), ProjectID: model.ProjectID.String(),
		Kind: model.Kind, Title: model.Title, Message: model.Body, ResourceType: model.ResourceType,
		ResourceID: model.ResourceID, CreatedAt: model.CreatedAt, ReadAt: model.ReadAt,
		ETag: notificationETag(model),
	}
}

func notificationETag(model storage.NotificationModel) string {
	return fmt.Sprintf(`"notification:%s:%d"`, model.ID, nullableTimeUnix(model.ReadAt))
}

func presenceValueKey(projectID, userID string) string {
	return "worksflow:presence:" + projectID + ":" + userID
}

func presenceIndexKey(projectID string) string {
	return "worksflow:presence:index:" + projectID
}
