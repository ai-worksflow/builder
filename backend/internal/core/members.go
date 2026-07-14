package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MemberUser struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	AvatarURL   *string   `json:"avatarUrl,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type ProjectMember struct {
	ProjectID string     `json:"projectId"`
	User      MemberUser `json:"user"`
	Role      Role       `json:"role"`
	JoinedAt  time.Time  `json:"joinedAt"`
	InvitedBy *string    `json:"invitedBy,omitempty"`
	ETag      string     `json:"etag"`
}

type ProjectInvitation struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
	// Token is returned only at creation so an email adapter or local developer
	// can deliver it. It is never persisted in plaintext or returned by list APIs.
	Token string `json:"token,omitempty"`
}

type MemberService struct {
	database *gorm.DB
	access   *AccessControl
	now      func() time.Time
}

func NewMemberService(database *gorm.DB, access *AccessControl) (*MemberService, error) {
	if database == nil || access == nil {
		return nil, errors.New("member database and access control are required")
	}
	return &MemberService{database: database, access: access, now: time.Now}, nil
}

func (s *MemberService) List(ctx context.Context, projectID, actorID string) ([]ProjectMember, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	type memberRow struct {
		ProjectID   uuid.UUID
		UserID      uuid.UUID
		Role        string
		JoinedAt    time.Time
		UpdatedAt   time.Time
		InvitedBy   *uuid.UUID
		Email       string
		DisplayName string
		AvatarURL   *string
		UserCreated time.Time `gorm:"column:user_created_at"`
	}
	var rows []memberRow
	err = s.database.WithContext(ctx).Table("project_members").
		Select("project_members.project_id, project_members.user_id, project_members.role, project_members.joined_at, project_members.updated_at, project_members.invited_by, users.email, users.display_name, users.avatar_url, users.created_at AS user_created_at").
		Joins("JOIN users ON users.id = project_members.user_id").
		Where("project_members.project_id = ?", projectUUID).
		Order("project_members.joined_at ASC").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list project members: %w", err)
	}
	result := make([]ProjectMember, 0, len(rows))
	for _, row := range rows {
		var invitedBy *string
		if row.InvitedBy != nil {
			value := row.InvitedBy.String()
			invitedBy = &value
		}
		result = append(result, ProjectMember{
			ProjectID: row.ProjectID.String(),
			User:      MemberUser{ID: row.UserID.String(), Email: row.Email, DisplayName: row.DisplayName, AvatarURL: row.AvatarURL, CreatedAt: row.UserCreated},
			Role:      Role(row.Role), JoinedAt: row.JoinedAt, InvitedBy: invitedBy,
			ETag: memberETag(row.ProjectID, row.UserID, row.Role, row.UpdatedAt),
		})
	}
	return result, nil
}

func (s *MemberService) AddExisting(ctx context.Context, projectID, actorID, email string, role Role) (ProjectMember, error) {
	actorRole, err := s.access.Authorize(ctx, projectID, actorID, ActionAdmin)
	if err != nil {
		return ProjectMember{}, err
	}
	if !ValidRole(role) || (role == RoleOwner && actorRole != RoleOwner) {
		return ProjectMember{}, ErrForbidden
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return ProjectMember{}, err
	}
	email, err = normalizeMemberEmail(email)
	if err != nil {
		return ProjectMember{}, err
	}
	var user storage.UserModel
	err = s.database.WithContext(ctx).Where("email = ? AND disabled_at IS NULL", email).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ProjectMember{}, ErrNotFound
	}
	if err != nil {
		return ProjectMember{}, fmt.Errorf("find invited user: %w", err)
	}
	now := s.now().UTC()
	member := storage.ProjectMemberModel{
		ProjectID: projectUUID, UserID: user.ID, Role: string(role), InvitedBy: &actorUUID,
		JoinedAt: now, UpdatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := lockProjectMembershipSet(transaction, projectUUID); err != nil {
			return err
		}
		currentRole, err := (&AccessControl{database: transaction}).Authorize(ctx, projectID, actorID, ActionAdmin)
		if err != nil {
			return err
		}
		if role == RoleOwner {
			if currentRole != RoleOwner {
				return ErrForbidden
			}
			governance, err := LoadProjectGovernance(ctx, transaction, projectUUID)
			if err != nil {
				return err
			}
			if governance.Mode == GovernanceModeSolo {
				var alreadyOwner int64
				if err := transaction.Model(&storage.ProjectMemberModel{}).
					Where("project_id = ? AND user_id = ? AND role = ?", projectUUID, user.ID, RoleOwner).
					Count(&alreadyOwner).Error; err != nil {
					return err
				}
				if alreadyOwner == 0 {
					return ErrSoloOwnerInvariant
				}
			}
		}
		if err := transaction.Clauses(clause.OnConflict{DoNothing: true}).Create(&member).Error; err != nil {
			return err
		}
		var stored storage.ProjectMemberModel
		if err := transaction.Where("project_id = ? AND user_id = ?", projectUUID, user.ID).Take(&stored).Error; err != nil {
			return err
		}
		member = stored
		if err := insertAudit(transaction, projectUUID, actorUUID, "project.member_added", "user", user.ID.String(), map[string]any{"role": member.Role}); err != nil {
			return err
		}
		if user.ID != actorUUID {
			if err := insertNotification(transaction, user.ID, projectUUID, "membership", "You were added to a project", "Your project role is "+member.Role+".", "project", projectID); err != nil {
				return err
			}
		}
		return enqueue(transaction, "project", projectID, "project.member_added", "worksflow.project.member.added", map[string]any{
			"projectId": projectID, "userId": user.ID.String(), "role": member.Role,
		})
	})
	if err != nil {
		return ProjectMember{}, fmt.Errorf("add project member: %w", err)
	}
	return memberFromModels(member, user), nil
}

func (s *MemberService) Invite(ctx context.Context, projectID, actorID, email string, role Role) (ProjectInvitation, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionAdmin); err != nil {
		return ProjectInvitation{}, err
	}
	if !ValidRole(role) || role == RoleOwner {
		return ProjectInvitation{}, fmt.Errorf("%w: invitation role", ErrInvalidInput)
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return ProjectInvitation{}, err
	}
	email, err = normalizeMemberEmail(email)
	if err != nil {
		return ProjectInvitation{}, err
	}
	var existing int64
	if err := s.database.WithContext(ctx).Table("project_members").
		Joins("JOIN users ON users.id = project_members.user_id").
		Where("project_members.project_id = ? AND users.email = ?", projectUUID, email).
		Count(&existing).Error; err != nil {
		return ProjectInvitation{}, err
	}
	if existing > 0 {
		return ProjectInvitation{}, ErrConflict
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return ProjectInvitation{}, fmt.Errorf("generate invitation token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	digest := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	model := storage.ProjectInvitationModel{
		ID: uuid.New(), ProjectID: projectUUID, Email: email, Role: string(role),
		TokenHash: digest[:], Status: "pending", InvitedBy: actorUUID,
		ExpiresAt: now.Add(7 * 24 * time.Hour), CreatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "project.invitation_created", "invitation", model.ID.String(), map[string]any{"email": email, "role": role}); err != nil {
			return err
		}
		return enqueue(transaction, "project", projectID, "project.invitation_created", "worksflow.project.invitation.created", map[string]any{
			"projectId": projectID, "invitationId": model.ID.String(), "email": email,
		})
	})
	if err != nil {
		return ProjectInvitation{}, fmt.Errorf("create invitation: %w", err)
	}
	return invitationFromModel(model, token), nil
}

func (s *MemberService) AcceptInvitation(ctx context.Context, actorID, token string) (ProjectMember, error) {
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return ProjectMember{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(token))
	if err != nil || len(decoded) != 32 {
		return ProjectMember{}, ErrNotFound
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	now := s.now().UTC()
	var invitation storage.ProjectInvitationModel
	var user storage.UserModel
	var member storage.ProjectMemberModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ? AND status = 'pending' AND expires_at > ?", digest[:], now).
			Take(&invitation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if err := lockProjectMembershipSet(transaction, invitation.ProjectID); err != nil {
			return err
		}
		if Role(invitation.Role) == RoleOwner {
			governance, err := LoadProjectGovernance(ctx, transaction, invitation.ProjectID)
			if err != nil {
				return err
			}
			if governance.Mode == GovernanceModeSolo {
				return ErrSoloOwnerInvariant
			}
		}
		if err := transaction.Where("id = ? AND disabled_at IS NULL", actorUUID).Take(&user).Error; err != nil {
			return err
		}
		if user.Email != invitation.Email {
			return ErrForbidden
		}
		member = storage.ProjectMemberModel{
			ProjectID: invitation.ProjectID, UserID: actorUUID, Role: invitation.Role,
			InvitedBy: &invitation.InvitedBy, JoinedAt: now, UpdatedAt: now,
		}
		if err := transaction.Clauses(clause.OnConflict{DoNothing: true}).Create(&member).Error; err != nil {
			return err
		}
		result := transaction.Model(&storage.ProjectInvitationModel{}).
			Where("id = ? AND status = 'pending'", invitation.ID).
			Updates(map[string]any{"status": "accepted", "accepted_by": actorUUID, "accepted_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return ErrConflict
		}
		if err := insertAudit(transaction, invitation.ProjectID, actorUUID, "project.invitation_accepted", "invitation", invitation.ID.String(), nil); err != nil {
			return err
		}
		return enqueue(transaction, "project", invitation.ProjectID.String(), "project.member_added", "worksflow.project.member.added", map[string]any{
			"projectId": invitation.ProjectID.String(), "userId": actorID, "role": invitation.Role,
		})
	})
	if err != nil {
		return ProjectMember{}, err
	}
	return memberFromModels(member, user), nil
}

func (s *MemberService) UpdateRole(ctx context.Context, projectID, actorID, memberID string, role Role, expectedETag string) (ProjectMember, error) {
	actorRole, err := s.access.Authorize(ctx, projectID, actorID, ActionAdmin)
	if err != nil {
		return ProjectMember{}, err
	}
	if !ValidRole(role) || (role == RoleOwner && actorRole != RoleOwner) {
		return ProjectMember{}, ErrForbidden
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return ProjectMember{}, err
	}
	memberUUID, err := uuid.Parse(memberID)
	if err != nil {
		return ProjectMember{}, fmt.Errorf("%w: member id", ErrInvalidInput)
	}
	now := s.now().UTC()
	var member storage.ProjectMemberModel
	var user storage.UserModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := lockProjectMembershipSet(transaction, projectUUID); err != nil {
			return err
		}
		currentRole, err := (&AccessControl{database: transaction}).Authorize(ctx, projectID, actorID, ActionAdmin)
		if err != nil {
			return err
		}
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND user_id = ?", projectUUID, memberUUID).Take(&member).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if expectedETag == "" || memberETag(member.ProjectID, member.UserID, member.Role, member.UpdatedAt) != expectedETag {
			return ErrConflict
		}
		if Role(member.Role) != RoleOwner && role == RoleOwner {
			if currentRole != RoleOwner {
				return ErrForbidden
			}
			governance, err := LoadProjectGovernance(ctx, transaction, projectUUID)
			if err != nil {
				return err
			}
			if governance.Mode == GovernanceModeSolo {
				return ErrSoloOwnerInvariant
			}
		}
		if Role(member.Role) == RoleOwner && role != RoleOwner {
			if currentRole != RoleOwner {
				return ErrForbidden
			}
			if err := ensureAnotherOwner(transaction, projectUUID, memberUUID); err != nil {
				return err
			}
		}
		member.Role = string(role)
		member.UpdatedAt = now
		if err := transaction.Save(&member).Error; err != nil {
			return err
		}
		if err := transaction.Where("id = ?", memberUUID).Take(&user).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "project.member_role_updated", "user", memberID, map[string]any{"role": role}); err != nil {
			return err
		}
		if memberUUID != actorUUID {
			if err := insertNotification(transaction, memberUUID, projectUUID, "membership", "Your project role changed", "Your new project role is "+string(role)+".", "project", projectID); err != nil {
				return err
			}
		}
		return enqueue(transaction, "project", projectID, "project.member_role_updated", "worksflow.project.member.updated", map[string]any{
			"projectId": projectID, "userId": memberID, "role": role,
		})
	})
	if err != nil {
		return ProjectMember{}, err
	}
	return memberFromModels(member, user), nil
}

func (s *MemberService) Remove(ctx context.Context, projectID, actorID, memberID, expectedETag string) error {
	_, err := s.access.Authorize(ctx, projectID, actorID, ActionAdmin)
	if err != nil {
		return err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return err
	}
	memberUUID, err := uuid.Parse(memberID)
	if err != nil {
		return fmt.Errorf("%w: member id", ErrInvalidInput)
	}
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := lockProjectMembershipSet(transaction, projectUUID); err != nil {
			return err
		}
		currentRole, err := (&AccessControl{database: transaction}).Authorize(ctx, projectID, actorID, ActionAdmin)
		if err != nil {
			return err
		}
		var member storage.ProjectMemberModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND user_id = ?", projectUUID, memberUUID).Take(&member).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if expectedETag == "" || memberETag(member.ProjectID, member.UserID, member.Role, member.UpdatedAt) != expectedETag {
			return ErrConflict
		}
		if Role(member.Role) == RoleOwner {
			if currentRole != RoleOwner {
				return ErrForbidden
			}
			if err := ensureAnotherOwner(transaction, projectUUID, memberUUID); err != nil {
				return err
			}
		}
		if err := transaction.Delete(&member).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "project.member_removed", "user", memberID, nil); err != nil {
			return err
		}
		return enqueue(transaction, "project", projectID, "project.member_removed", "worksflow.project.member.removed", map[string]any{
			"projectId": projectID, "userId": memberID,
		})
	})
}

func lockProjectMembershipSet(transaction *gorm.DB, projectID uuid.UUID) error {
	var project storage.ProjectModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("id = ?", projectID).Take(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func ensureAnotherOwner(transaction *gorm.DB, projectID, excludedUserID uuid.UUID) error {
	var count int64
	if err := transaction.Model(&storage.ProjectMemberModel{}).
		Where("project_id = ? AND role = ? AND user_id <> ?", projectID, RoleOwner, excludedUserID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrLastOwner
	}
	return nil
}

func normalizeMemberEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 254 || strings.Count(value, "@") != 1 {
		return "", fmt.Errorf("%w: email", ErrInvalidInput)
	}
	return value, nil
}

func memberFromModels(member storage.ProjectMemberModel, user storage.UserModel) ProjectMember {
	var invitedBy *string
	if member.InvitedBy != nil {
		value := member.InvitedBy.String()
		invitedBy = &value
	}
	return ProjectMember{
		ProjectID: member.ProjectID.String(),
		User:      MemberUser{ID: user.ID.String(), Email: user.Email, DisplayName: user.DisplayName, AvatarURL: user.AvatarURL, CreatedAt: user.CreatedAt},
		Role:      Role(member.Role), JoinedAt: member.JoinedAt, InvitedBy: invitedBy,
		ETag: memberETag(member.ProjectID, member.UserID, member.Role, member.UpdatedAt),
	}
}

func invitationFromModel(model storage.ProjectInvitationModel, token string) ProjectInvitation {
	return ProjectInvitation{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), Email: model.Email,
		Role: Role(model.Role), Status: model.Status, ExpiresAt: model.ExpiresAt,
		CreatedAt: model.CreatedAt, Token: token,
	}
}

func memberETag(projectID, userID uuid.UUID, role string, updatedAt time.Time) string {
	return fmt.Sprintf(`"member:%s:%s:%s:%d"`, projectID, userID, role, updatedAt.UnixNano())
}
