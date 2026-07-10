package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type Role string

const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleEditor    Role = "editor"
	RoleCommenter Role = "commenter"
	RoleViewer    Role = "viewer"
)

type Action string

const (
	ActionView    Action = "view"
	ActionComment Action = "comment"
	ActionEdit    Action = "edit"
	ActionReview  Action = "review"
	ActionApprove Action = "approve"
	ActionPublish Action = "publish"
	ActionAdmin   Action = "admin"
)

var roleActions = map[Role]map[Action]struct{}{
	RoleOwner:     actions(ActionView, ActionComment, ActionEdit, ActionReview, ActionApprove, ActionPublish, ActionAdmin),
	RoleAdmin:     actions(ActionView, ActionComment, ActionEdit, ActionReview, ActionApprove, ActionPublish, ActionAdmin),
	RoleEditor:    actions(ActionView, ActionComment, ActionEdit, ActionReview),
	RoleCommenter: actions(ActionView, ActionComment),
	RoleViewer:    actions(ActionView),
}

func actions(values ...Action) map[Action]struct{} {
	result := make(map[Action]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func ValidRole(role Role) bool {
	_, ok := roleActions[role]
	return ok
}

type AccessControl struct {
	database *gorm.DB
}

func NewAccessControl(database *gorm.DB) (*AccessControl, error) {
	if database == nil {
		return nil, errors.New("access-control database is required")
	}
	return &AccessControl{database: database}, nil
}

func (a *AccessControl) Authorize(ctx context.Context, projectID, userID string, action Action) (Role, error) {
	projectUUID, userUUID, err := parseProjectUser(projectID, userID)
	if err != nil {
		return "", err
	}
	var member storage.ProjectMemberModel
	err = a.database.WithContext(ctx).
		Where("project_id = ? AND user_id = ?", projectUUID, userUUID).
		Take(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load project membership: %w", err)
	}
	role := Role(member.Role)
	if _, allowed := roleActions[role][action]; !allowed {
		return role, ErrForbidden
	}
	return role, nil
}

func parseProjectUser(projectID, userID string) (uuid.UUID, uuid.UUID, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: user id", ErrInvalidInput)
	}
	return projectUUID, userUUID, nil
}
